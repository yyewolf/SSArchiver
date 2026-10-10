package beatleader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

const (
	DefaultBaseURL = "https://api.beatleader.xyz"
	ScoresPageSize = 100
)

// ReplayPrefixes are the only URL prefixes replays are fetched from (spec §5.3).
type ReplayPrefixes struct {
	CDN     string // score replays on the CDN (CDN limiter)
	Storage string // score replays served by the API host (API limiter)
	Other   string // attempt replays served by the API host (API limiter)
}

var DefaultReplayPrefixes = ReplayPrefixes{
	CDN:     "https://cdn.replays.beatleader.xyz/",
	Storage: "https://api.beatleader.xyz/replays-storage/",
	Other:   "https://api.beatleader.xyz/otherreplays/",
}

var (
	ErrNotFound     = platform.ErrNotFound
	ErrRateLimited  = platform.ErrRateLimited
	ErrUnauthorized = platform.ErrUnauthorized
	ErrReplayURL    = errors.New("beatleader: replay URL not on the allowlist")
)

// StatusError is returned for unexpected non-2xx responses.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("beatleader: unexpected status %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL   string
	prefixes  ReplayPrefixes
	hc        *http.Client
	api, cdn  *Limiter
	userAgent string
}

type Option func(*Client)

// WithBaseURL points the client at another server. The replay prefixes move
// under it too ({u}/cdn-replays/, {u}/replays-storage/, {u}/otherreplays/) so
// one fake server can stand in for every BeatLeader host in tests.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = u
		c.prefixes = ReplayPrefixes{CDN: u + "/cdn-replays/", Storage: u + "/replays-storage/", Other: u + "/otherreplays/"}
	}
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// NewClient returns a client whose API calls go through api and CDN replay
// downloads through cdn (nil limiters: not rate limited).
func NewClient(api, cdn *Limiter, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL, prefixes: DefaultReplayPrefixes,
		hc:  &http.Client{Timeout: 2 * time.Minute},
		api: api, cdn: cdn, userAgent: buildinfo.UserAgent(),
	}
	for _, o := range opts {
		o(c)
	}
	hc := *c.hc // never mutate the caller's client
	hc.CheckRedirect = c.checkRedirect
	c.hc = &hc
	return c
}

// checkRedirect only follows redirects that stay on the replay allowlist.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("beatleader: too many redirects")
	}
	if !c.ReplayAllowed(req.URL.String()) {
		return fmt.Errorf("%w: redirect to %s", ErrReplayURL, req.URL.Redacted())
	}
	return nil
}

// ReplayAllowed reports whether u is under one of the replay prefixes.
func (c *Client) ReplayAllowed(u string) bool {
	for _, p := range []string{c.prefixes.CDN, c.prefixes.Storage, c.prefixes.Other} {
		if p != "" && strings.HasPrefix(u, p) && !strings.Contains(u[len(p):], "..") {
			return true
		}
	}
	return false
}

// Player fetches a player's profile. BeatLeader maps alternate account IDs to
// the main one: store the returned ID, not the one asked for.
func (c *Client) Player(ctx context.Context, id string) (Player, error) {
	var p Player
	err := c.getJSON(ctx, "/player/"+url.PathEscape(id), url.Values{"stats": {"false"}}, &p)
	return p, err
}

// Scores fetches one page (newest first) of a player's scores: one per
// leaderboard, the current personal best. 404 for unknown players and for
// players without scores.
func (c *Client) Scores(ctx context.Context, playerID string, page int) (ScorePage, error) {
	var sp ScorePage
	q := url.Values{
		"sortBy": {"date"}, "order": {"desc"},
		"page": {strconv.Itoa(page)}, "count": {strconv.Itoa(ScoresPageSize)},
	}
	err := c.getJSON(ctx, "/player/"+url.PathEscape(playerID)+"/scores", q, &sp)
	return sp, err
}

// Attempts fetches one page (newest first) of a player's attempts
// (scoresstats; sortBy must be sent, it defaults to pp). 401 while the
// player's history is private, and for unknown players (spec §2.2).
func (c *Client) Attempts(ctx context.Context, playerID string, page, count int) (ScorePage, error) {
	var sp ScorePage
	q := url.Values{
		"sortBy": {"date"}, "order": {"desc"},
		"page": {strconv.Itoa(page)}, "count": {strconv.Itoa(count)},
	}
	err := c.getJSON(ctx, "/player/"+url.PathEscape(playerID)+"/scoresstats", q, &sp)
	return sp, err
}

// ScoreReplay reports whether u is a score replay (CDN or replays-storage)
// rather than an attempt replay (otherreplays).
func (c *Client) ScoreReplay(u string) bool {
	return c.ReplayAllowed(u) && !strings.HasPrefix(u, c.prefixes.Other)
}

// Replay streams a replay from an allowlisted URL. The caller closes the body.
func (c *Client) Replay(ctx context.Context, u string) (io.ReadCloser, error) {
	if !c.ReplayAllowed(u) {
		return nil, fmt.Errorf("%w: %q", ErrReplayURL, u)
	}
	l := c.api
	if strings.HasPrefix(u, c.prefixes.CDN) {
		l = c.cdn
	}
	resp, err := c.do(ctx, l, u)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) do(ctx context.Context, l *Limiter, u string) (*http.Response, error) {
	if l != nil {
		if err := l.Wait(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("beatleader: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	path := req.URL.Path
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("beatleader: GET %s: %w", path, err)
	}
	if l != nil {
		l.Observe(resp.Header, resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return resp, nil
	case http.StatusNotFound:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	case http.StatusUnauthorized, http.StatusForbidden:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, path)
	case http.StatusTooManyRequests:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, path)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, c.api, c.baseURL+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("beatleader: decode %s: %w", path, err)
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
