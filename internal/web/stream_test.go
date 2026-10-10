package web_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

type sseEvent struct{ name, data string }

// sseStream reads server-sent events from a live test server.
type sseStream struct {
	t  *testing.T
	br *bufio.Reader
}

func openStream(t *testing.T, e *testEnv, path string, cookie *http.Cookie) *sseStream {
	t.Helper()
	ts := httptest.NewServer(e.h)
	t.Cleanup(ts.Close)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	return &sseStream{t: t, br: bufio.NewReader(resp.Body)}
}

// event reads the next event; heartbeats (": ping") are skipped.
func (s *sseStream) event() sseEvent {
	s.t.Helper()
	type res struct {
		name, data string
		err        error
	}
	ch := make(chan res, 1)
	go func() {
		n, d, err := readSSEFrame(s.br)
		ch <- res{n, d, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			s.t.Fatalf("read event: %v", r.err)
		}
		return sseEvent{name: r.name, data: r.data}
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for an SSE event")
		return sseEvent{}
	}
}

func readSSEFrame(br *bufio.Reader) (name, data string, err error) {
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			if rerr == io.EOF && name != "" {
				return name, data, nil
			}
			return "", "", rerr
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if name != "" {
				return name, data, nil
			}
		case strings.HasPrefix(line, ":"):
			// heartbeat comment
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			if data == "" {
				data = d
			} else {
				data += "\n" + d
			}
		}
	}
}

func TestPublicStreamPlayerSnapshotAndUpdates(t *testing.T) {
	e := newEnv(t)
	e.setup()
	id := e.seed()
	s := openStream(t, e, "/stream?player="+id, nil)

	stats := s.event()
	accounts := s.event()
	progress := s.event()
	if stats.name != "stats" || !strings.Contains(stats.data, `id="player-stats"`) {
		t.Fatalf("stats snapshot = %q %q", stats.name, stats.data)
	}
	if accounts.name != "accounts" || !strings.Contains(accounts.data, "1 / 2 replays") {
		t.Fatalf("accounts snapshot = %q %q", accounts.name, accounts.data)
	}
	if progress.name != "progress" || !strings.Contains(progress.data, "1 of 2 replays archived") {
		t.Fatalf("progress snapshot = %q %q", progress.name, progress.data)
	}

	// A replay event for another player must not reach this stream.
	e.svc.Log(context.Background(), model.SyncEvent{Kind: model.KindReplay, PlayerID: new("p00000000999"), Message: "someone else"})
	e.svc.Log(context.Background(), model.SyncEvent{Kind: model.KindReplay, PlayerID: new(id), Message: "archived replay"})

	stats, accounts, progress = s.event(), s.event(), s.event()
	if stats.name != "stats" || accounts.name != "accounts" || progress.name != "progress" {
		t.Fatalf("update events = %q %q %q", stats.name, accounts.name, progress.name)
	}
	if !strings.Contains(progress.data, "1 of 2 replays archived") {
		t.Fatalf("progress update = %q", progress.data)
	}
}

func TestPublicStreamHomeCards(t *testing.T) {
	e := newEnv(t)
	e.setup()
	id := e.seed()
	s := openStream(t, e, "/stream", nil)

	card := s.event()
	if card.name != "card/"+id {
		t.Fatalf("card snapshot event = %q", card.name)
	}
	if !strings.Contains(card.data, `sse-swap="card/`+id+`"`) {
		t.Fatalf("card snapshot = %q", card.data)
	}

	e.svc.Log(context.Background(), model.SyncEvent{Kind: model.KindScores, PlayerID: new(id), Message: "2 new scores"})
	card = s.event()
	if card.name != "card/"+id || !strings.Contains(card.data, `id="player-`+id+`"`) {
		t.Fatalf("card update = %q %q", card.name, card.data)
	}
}

func TestPublicStreamUnknownPlayer(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream?player=p99999999999", nil)
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAdminStreamRequiresLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/sync/stream", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous stream code = %d, want 401", rec.Code)
	}
}

func TestAdminStreamSnapshotAndUpdates(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	ctx := context.Background()
	s := openStream(t, e, "/admin/sync/stream", c)

	sync, events, failed := s.event(), s.event(), s.event()
	if sync.name != "sync" || !strings.Contains(sync.data, `id="sync-live"`) {
		t.Fatalf("sync snapshot = %q %q", sync.name, sync.data)
	}
	if events.name != "events" || !strings.Contains(events.data, `id="sync-events"`) {
		t.Fatalf("events snapshot = %q %q", events.name, events.data)
	}
	if failed.name != "failed" || !strings.Contains(failed.data, `id="failed-replays"`) {
		t.Fatalf("failed snapshot = %q %q", failed.name, failed.data)
	}

	id := e.playerID("1001")
	// A replay event also refreshes the failed replays table.
	e.svc.Log(ctx, model.SyncEvent{Kind: model.KindReplay, PlayerID: new(id), Message: "download failed"})
	sync, events, failed = s.event(), s.event(), s.event()
	if sync.name != "sync" || events.name != "events" || failed.name != "failed" {
		t.Fatalf("replay update events = %q %q %q", sync.name, events.name, failed.name)
	}
	if !strings.Contains(events.data, "download failed") {
		t.Fatalf("events update misses the new event: %q", events.data)
	}

	// A poll event refreshes the live section and the activity table only.
	e.svc.Log(ctx, model.SyncEvent{Kind: model.KindPoll, PlayerID: new(id), Message: "polled"})
	sync, events = s.event(), s.event()
	if sync.name != "sync" || events.name != "events" {
		t.Fatalf("poll update events = %q %q", sync.name, events.name)
	}
	select {
	case ev := <-readAsync(s):
		s.t.Fatalf("poll update must not touch the failed table, got %q", ev.name)
	case <-time.After(300 * time.Millisecond):
	}
}

// readAsync exposes the next event read in the background so tests can
// assert that nothing arrives; a closed stream (test cleanup) reports nothing.
func readAsync(s *sseStream) <-chan sseEvent {
	ch := make(chan sseEvent, 1)
	go func() {
		name, data, err := readSSEFrame(s.br)
		if err != nil {
			return
		}
		ch <- sseEvent{name: name, data: data}
	}()
	return ch
}
