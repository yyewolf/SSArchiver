package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// SessionGap is the pause that ends a play session: a play set more than
// SessionGap after the previous one starts a new session.
const SessionGap = 2 * time.Hour

// MapGroup is one row of the merged player page (spec §6.1): the plays of one
// map (map_key) within one session that match the filter.
type MapGroup struct {
	MapKey string
	Latest *model.Score   // the newest matching play; shows the map
	Chips  []*model.Score // each platform's best score of the session, primary platform first
	Plays  int64          // matching plays of this map in the session
}

// More is how many matching plays the chips do not show.
func (g MapGroup) More() int64 { return max(g.Plays-int64(len(g.Chips)), 0) }

// Session is a run of a player's plays with no pause longer than SessionGap.
// Its bounds cover every play, whatever the filter; the counts and groups
// only the matching ones.
type Session struct {
	Start, End time.Time // first and last play
	Plays      int64     // matching plays
	Maps       int64     // matching maps
	Continued  bool      // its newer maps are on the previous page
	Groups     []MapGroup
	sid        int64
}

func (s Session) Duration() time.Duration { return s.End.Sub(s.Start) }

type SessionList struct {
	Sessions []Session
	Total    int64 // matching maps, counted once per session
	Page     int
	PerPage  int
	Pages    int
}

// sessionsCTE numbers every play of a player by session (sess.sid, 1 for the
// oldest); join it with "JOIN sess ON sess.id = s.id".
const sessionsCTE = "WITH times AS (SELECT id, unixepoch(set_at) AS t FROM scores WHERE player_id = ?), " +
	"gaps AS (SELECT id, t, CASE WHEN t - LAG(t) OVER (ORDER BY t, id) <= ? THEN 0 ELSE 1 END AS fresh FROM times), " +
	"sess AS (SELECT id, SUM(fresh) OVER (ORDER BY t, id) AS sid FROM gaps) "

func sessionArgs(playerID string) []any { return []any{playerID, int64(SessionGap / time.Second)} }

// rfc3339Of is the SQL for the earliest (MIN) or latest (MAX) s.set_at as
// RFC 3339 text, whatever layout each row was stored in.
func rfc3339Of(agg string) string {
	return "strftime('%Y-%m-%dT%H:%M:%fZ', " + agg + "(julianday(s.set_at)))"
}

// sessionPlaysFrom is playsFrom with each play's session.
const sessionPlaysFrom = " FROM scores s JOIN sess ON sess.id = s.id JOIN leaderboards lb ON lb.id = s.leaderboard_id WHERE "

// ListSessions pages over a player's maps per session, newest session and
// newest play first. Sessions are cut over every play of the player; a map is
// listed in a session when any of its plays there matches f. Chips are each
// platform's best score of that session on that map (restricted to
// f.Platform when set), whatever the other filters.
func (s *Service) ListSessions(ctx context.Context, f ScoreFilter) (SessionList, error) {
	if f.PlayerID == "" {
		return SessionList{}, errors.New("service: sessions need a player")
	}
	f = f.normalized()
	where, args := f.where()
	cteArgs := sessionArgs(f.PlayerID)
	gdb := s.db.WithContext(ctx)
	out := SessionList{Page: f.Page, PerPage: f.PerPage}
	if err := gdb.Raw(sessionsCTE+"SELECT COUNT(*) FROM (SELECT 1"+sessionPlaysFrom+where+" GROUP BY sess.sid, lb.map_key)",
		slices.Concat(cteArgs, args)...).Scan(&out.Total).Error; err != nil {
		return out, fmt.Errorf("service: count session maps: %w", err)
	}
	out.Pages = max(int((out.Total+int64(f.PerPage)-1)/int64(f.PerPage)), 1)

	type groupRow struct {
		SID    int64 `gorm:"column:sid"`
		MapKey string
		Plays  int64
		Pos    int64 // 1 for the session's newest map
	}
	var rows []groupRow
	if err := gdb.Raw(sessionsCTE+"SELECT sess.sid AS sid, lb.map_key AS map_key, COUNT(*) AS plays, "+
		"ROW_NUMBER() OVER (PARTITION BY sess.sid ORDER BY MAX(s.set_at) DESC, lb.map_key) AS pos"+sessionPlaysFrom+where+
		" GROUP BY sess.sid, lb.map_key ORDER BY sess.sid DESC, MAX(s.set_at) DESC, lb.map_key LIMIT ? OFFSET ?",
		slices.Concat(cteArgs, args, []any{f.PerPage, (f.Page - 1) * f.PerPage})...).Scan(&rows).Error; err != nil {
		return out, fmt.Errorf("service: list session maps: %w", err)
	}
	if len(rows) == 0 {
		return out, nil
	}
	var sids []int64
	var keys []string
	for _, r := range rows {
		if !slices.Contains(sids, r.SID) {
			sids = append(sids, r.SID)
		}
		if !slices.Contains(keys, r.MapKey) {
			keys = append(keys, r.MapKey)
		}
	}

	sessions, err := s.sessionHeads(ctx, f, sids)
	if err != nil {
		return out, err
	}

	type pick struct {
		ID     int64
		SID    int64 `gorm:"column:sid"`
		MapKey string
	}
	var latest []pick
	if err := gdb.Raw(sessionsCTE+"SELECT id, sid, map_key FROM (SELECT s.id AS id, sess.sid AS sid, lb.map_key AS map_key, "+
		"ROW_NUMBER() OVER (PARTITION BY sess.sid, lb.map_key ORDER BY s.set_at DESC, s.id DESC) AS rn"+
		sessionPlaysFrom+where+" AND sess.sid IN ? AND lb.map_key IN ?) WHERE rn = 1",
		slices.Concat(cteArgs, args, []any{sids, keys})...).Scan(&latest).Error; err != nil {
		return out, fmt.Errorf("service: newest plays: %w", err)
	}
	bestWhere := "s.player_id = ? AND s.kind = ? AND sess.sid IN ? AND lb.map_key IN ?"
	chipArgs := []any{f.PlayerID, model.KindScore, sids, keys}
	if f.Platform != "" {
		bestWhere += " AND s.platform = ?"
		chipArgs = append(chipArgs, f.Platform)
	}
	var chips []pick
	if err := gdb.Raw(sessionsCTE+"SELECT id, sid, map_key FROM (SELECT s.id AS id, sess.sid AS sid, lb.map_key AS map_key, "+
		"ROW_NUMBER() OVER (PARTITION BY sess.sid, lb.map_key, s.platform ORDER BY s.modified_score DESC, s.set_at DESC, s.id DESC) AS rn"+
		sessionPlaysFrom+bestWhere+") WHERE rn = 1", slices.Concat(cteArgs, chipArgs)...).Scan(&chips).Error; err != nil {
		return out, fmt.Errorf("service: session bests: %w", err)
	}

	ids := make([]int64, 0, len(latest)+len(chips))
	for _, p := range slices.Concat(latest, chips) {
		ids = append(ids, p.ID)
	}
	loaded, err := s.loadScores(ctx, ids)
	if err != nil {
		return out, err
	}
	byID := make(map[int64]*model.Score, len(loaded))
	for _, sc := range loaded {
		byID[sc.ID] = sc
	}
	type groupKey struct {
		sid    int64
		mapKey string
	}
	newest := map[groupKey]*model.Score{}
	for _, p := range latest {
		newest[groupKey{p.SID, p.MapKey}] = byID[p.ID]
	}
	best := map[groupKey][]*model.Score{}
	for _, p := range chips {
		if sc := byID[p.ID]; sc != nil {
			k := groupKey{p.SID, p.MapKey}
			best[k] = append(best[k], sc)
		}
	}
	for _, r := range rows {
		if n := len(out.Sessions); n == 0 || out.Sessions[n-1].sid != r.SID {
			sess := sessions[r.SID]
			sess.Continued = r.Pos > 1
			out.Sessions = append(out.Sessions, sess)
		}
		k := groupKey{r.SID, r.MapKey}
		c := best[k]
		slices.SortFunc(c, func(a, b *model.Score) int {
			return cmp.Or(cmp.Compare(s.platformOrder(a.Platform), s.platformOrder(b.Platform)), strings.Compare(a.Platform, b.Platform))
		})
		cur := &out.Sessions[len(out.Sessions)-1]
		cur.Groups = append(cur.Groups, MapGroup{MapKey: r.MapKey, Latest: newest[k], Chips: c, Plays: r.Plays})
	}
	return out, nil
}

// sessionHeads loads the bounds (over every play) and the matching counts of
// sessions sids.
func (s *Service) sessionHeads(ctx context.Context, f ScoreFilter, sids []int64) (map[int64]Session, error) {
	where, args := f.where()
	cteArgs := sessionArgs(f.PlayerID)
	gdb := s.db.WithContext(ctx)
	var bounds []struct {
		SID   int64  `gorm:"column:sid"`
		Start string `gorm:"column:started"`
		End   string `gorm:"column:ended"`
	}
	if err := gdb.Raw(sessionsCTE+"SELECT sess.sid AS sid, "+rfc3339Of("MIN")+" AS started, "+rfc3339Of("MAX")+" AS ended"+
		" FROM scores s JOIN sess ON sess.id = s.id WHERE sess.sid IN ? GROUP BY sess.sid",
		slices.Concat(cteArgs, []any{sids})...).Scan(&bounds).Error; err != nil {
		return nil, fmt.Errorf("service: session bounds: %w", err)
	}
	var counts []struct {
		SID         int64 `gorm:"column:sid"`
		Plays, Maps int64
	}
	if err := gdb.Raw(sessionsCTE+"SELECT sess.sid AS sid, COUNT(*) AS plays, COUNT(DISTINCT lb.map_key) AS maps"+
		sessionPlaysFrom+where+" AND sess.sid IN ? GROUP BY sess.sid",
		slices.Concat(cteArgs, args, []any{sids})...).Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("service: session counts: %w", err)
	}
	out := make(map[int64]Session, len(sids))
	for _, b := range bounds {
		start, err := time.Parse(time.RFC3339Nano, b.Start)
		if err != nil {
			return nil, fmt.Errorf("service: session start %q: %w", b.Start, err)
		}
		end, err := time.Parse(time.RFC3339Nano, b.End)
		if err != nil {
			return nil, fmt.Errorf("service: session end %q: %w", b.End, err)
		}
		out[b.SID] = Session{sid: b.SID, Start: start.UTC(), End: end.UTC()}
	}
	for _, c := range counts {
		sess := out[c.SID]
		sess.Plays, sess.Maps = c.Plays, c.Maps
		out[c.SID] = sess
	}
	return out, nil
}

// GetPlay finds a row by its platform, kind and platform ID (spec §6.1).
func (s *Service) GetPlay(ctx context.Context, platformName, kind, externalID string) (*model.Score, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).
		Where(q.Platform.Eq(platformName), q.Kind.Eq(kind), q.ExternalID.Eq(externalID)).First()
	if err != nil {
		return nil, notFound(err, platformName+" "+kind+" "+externalID)
	}
	return sc, nil
}
