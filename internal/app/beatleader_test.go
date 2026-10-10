package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/app"
)

var (
	bsorBytes    = []byte("BSOR e2e payload")
	attemptBytes = []byte("BSOR attempt e2e payload")
)

// fakeBeatLeader serves one player with one score whose replay sits under
// replays-storage (beatleader.WithBaseURL maps the replay hosts onto it).
// The map is the one fakeScoreSaber's score 777 is on: hash ABC, Expert+.
// The attempts history (scoresstats) answers 401 until public is true.
func fakeBeatLeader(t *testing.T, public *atomic.Bool) *httptest.Server {
	t.Helper()
	set := time.Now().UTC().Add(time.Hour).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /player/76561198038925092", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"76561198038925092","name":"Yewolf","avatar":"https://cdn.assets.beatleader.xyz/a.png","country":"FR"}`))
	})
	mux.HandleFunc("GET /player/76561198038925092/scores", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"metadata":{"page":1,"itemsPerPage":100,"total":1},"data":[{"id":888,"baseScore":1050,"modifiedScore":1050,
			"accuracy":0.96,"pp":0,"rank":3,"modifiers":"","hmd":256,"timeset":"%d","timepost":%d,"leaderboardId":"abc71",
			"replay":"http://%s/replays-storage/888-76561198038925092-ExpertPlus-Standard-ABC.bsor",
			"leaderboard":{"id":"abc71","song":{"hash":"abc","name":"E2E Song","subName":"","author":"A","mapper":"M","coverImage":""},
			"difficulty":{"value":9,"modeName":"Standard","difficultyName":"ExpertPlus","status":0,"stars":null,"maxScore":1100}}}]}`, set, set, r.Host)
	})
	mux.HandleFunc("GET /replays-storage/888-76561198038925092-ExpertPlus-Standard-ABC.bsor", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bsorBytes)
	})
	// Attempts: a fail with its own replay, and a clear that is the PB score
	// (its replay is the score's replays-storage file): the clear is skipped.
	mux.HandleFunc("GET /player/76561198038925092/scoresstats", func(w http.ResponseWriter, r *http.Request) {
		if !public.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		lb := `"leaderboardId":"abc71","leaderboard":{"id":"abc71","song":{"hash":"abc","name":"E2E Song","subName":"","author":"A","mapper":"M","coverImage":""},` +
			`"difficulty":{"value":9,"modeName":"Standard","difficultyName":"ExpertPlus","status":0,"stars":null,"maxScore":1100}}`
		fmt.Fprintf(w, `{"metadata":{"page":1,"itemsPerPage":100,"total":2},"data":[
			{"id":991,"endType":2,"time":42.5,"timepost":%d,"timeset":null,"baseScore":300,"modifiedScore":300,"accuracy":0.8,"pp":0,"rank":0,"modifiers":"","hmd":256,
			 "replay":"http://%s/otherreplays/991.bsor",%s},
			{"id":992,"endType":1,"time":120,"timepost":%d,"timeset":null,"baseScore":1050,"modifiedScore":1050,"accuracy":0.96,"pp":0,"rank":3,"modifiers":"","hmd":256,
			 "replay":"http://%s/replays-storage/888-76561198038925092-ExpertPlus-Standard-ABC.bsor",%s}]}`,
			set+60, r.Host, lb, set, r.Host, lb)
	})
	mux.HandleFunc("GET /otherreplays/991.bsor", func(w http.ResponseWriter, r *http.Request) {
		if !public.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(attemptBytes)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBeatLeaderEndToEnd(t *testing.T) {
	r := startApp(t, app.Options{ScoreSaberURL: fakeScoreSaber(t).URL, BeatLeaderURL: fakeBeatLeader(t, new(atomic.Bool)).URL})

	code, body := r.do(t, http.MethodPost, "/api/v1/players", `{"ref":"https://beatleader.com/u/76561198038925092"}`)
	if code != http.StatusCreated {
		t.Fatalf("add from a BeatLeader URL: %d %s", code, body)
	}
	var p struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Identities []struct {
			Platform string `json:"platform"`
		} `json:"identities"`
	}
	_ = json.Unmarshal(body, &p)
	if p.Name != "Yewolf" || len(p.Identities) != 1 || p.Identities[0].Platform != "beatleader" {
		t.Fatalf("player = %+v", p)
	}
	code, body = r.do(t, http.MethodPost, "/api/v1/players/"+p.ID+"/identities", `{"platform":"scoresaber","ref":"1001"}`)
	if code != http.StatusOK || !strings.Contains(string(body), `"platform":"scoresaber"`) {
		t.Fatalf("link ScoreSaber: %d %s", code, body)
	}

	r.waitArchived(t, "/api/v1/scores/beatleader/888")
	r.waitArchived(t, "/api/v1/scores/777")

	for path, want := range map[string][]byte{"/r/bl/888.bsor": bsorBytes, "/r/777.dat": replayBytes} {
		status, got, err := getBody(r.Client, r.Base+path)
		if err != nil || status != 200 || !bytes.Equal(got, want) {
			t.Fatalf("GET %s: %d %q %v", path, status, got, err)
		}
	}
	status, _, _ := getBody(r.Client, r.Base+"/s/bl/888")
	if status != 200 {
		t.Fatalf("score page = %d", status)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, r.Base+"/p/bl/76561198038925092", nil)
	res, err := r.Client.Do(req)
	if err != nil || res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "/p/"+p.ID {
		t.Fatalf("account link: %v %v", res, err)
	}
	res.Body.Close()

	_, page, _ := getBody(r.Client, r.Base+"/p/"+p.ID)
	html := string(page)
	if strings.Count(html, "E2E Song") != 1 || !strings.Contains(html, `href="/s/777"`) || !strings.Contains(html, `href="/s/bl/888"`) {
		t.Fatalf("one row for the map played on both platforms, with both chips:\n%s", html)
	}

	code, body = r.do(t, http.MethodGet, "/api/v1/sync", "")
	if code != 200 || !strings.Contains(string(body), `"name":"beatleader/api"`) {
		t.Fatalf("sync status: %d %s", code, body)
	}
}

func TestBeatLeaderAttemptsEndToEnd(t *testing.T) {
	public := new(atomic.Bool)
	r := startApp(t, app.Options{ScoreSaberURL: fakeScoreSaber(t).URL, BeatLeaderURL: fakeBeatLeader(t, public).URL})

	code, body := r.do(t, http.MethodPost, "/api/v1/players", `{"ref":"https://beatleader.com/u/76561198038925092"}`)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %s", code, body)
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &p)
	feed := "/api/v1/players/" + p.ID + "/identities/beatleader/feeds/attempt"

	code, body = r.do(t, http.MethodPatch, feed, `{"enabled":true}`)
	if code != 200 || !strings.Contains(string(body), `"access":"private"`) ||
		!strings.Contains(string(body), `"title":"Attempt history is private on BeatLeader"`) {
		t.Fatalf("switch on while private: %d %s", code, body)
	}

	public.Store(true) // the player turned "Public history (auto-synced)" on
	code, body = r.do(t, http.MethodPost, feed+"/check", "")
	if code != 200 || !strings.Contains(string(body), `"access":"public"`) || !strings.Contains(string(body), `"remote_total":2`) {
		t.Fatalf("check again: %d %s", code, body)
	}

	r.waitArchived(t, "/api/v1/scores/beatleader/attempt/991")
	if status, got, err := getBody(r.Client, r.Base+"/r/bl/attempt/991.bsor"); err != nil || status != 200 || !bytes.Equal(got, attemptBytes) {
		t.Fatalf("attempt replay: %d %q %v", status, got, err)
	}
	if status, page, _ := getBody(r.Client, r.Base+"/s/bl/attempt/991"); status != 200 || !strings.Contains(string(page), "Failed") {
		t.Fatalf("attempt page: %d", status)
	}
	if status, _, _ := getBody(r.Client, r.Base+"/api/v1/scores/beatleader/attempt/992"); status != 404 {
		t.Fatalf("the PB clear is skipped, not stored: %d", status)
	}
	for q, want := range map[string]string{"": `"total":1`, "?type=fail": `"total":1`, "?type=all": `"total":2`} {
		_, list, _ := getBody(r.Client, r.Base+"/api/v1/players/"+p.ID+"/scores"+q)
		if !strings.Contains(string(list), want) {
			t.Errorf("scores%s = %s, want %s", q, list, want)
		}
	}
}
