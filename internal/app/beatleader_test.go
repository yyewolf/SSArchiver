package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/app"
)

var bsorBytes = []byte("BSOR e2e payload")

// fakeBeatLeader serves one player with one score whose replay sits under
// replays-storage (beatleader.WithBaseURL maps the replay hosts onto it).
// The map is the one fakeScoreSaber's score 777 is on: hash ABC, Expert+.
func fakeBeatLeader(t *testing.T) *httptest.Server {
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBeatLeaderEndToEnd(t *testing.T) {
	r := startApp(t, app.Options{ScoreSaberURL: fakeScoreSaber(t).URL, BeatLeaderURL: fakeBeatLeader(t).URL})

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
