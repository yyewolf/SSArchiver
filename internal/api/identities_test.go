package api_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestIdentityEndpoints(t *testing.T) {
	svc, h := newMultiAPI(t, true)
	alice := seed(t, svc)
	base := "/api/v1/players/" + alice + "/identities"

	code, p, _ := call(t, h, http.MethodPost, base, map[string]any{"platform": "testplat", "ref": "https://tp.example/u/abc"})
	ids, _ := p["identities"].([]any)
	if code != 200 || len(ids) != 2 || ids[1].(map[string]any)["platform"] != "testplat" || ids[1].(map[string]any)["id"] != "abc" {
		t.Fatalf("link = %d %v", code, p)
	}
	if code, body, _ := call(t, h, http.MethodPost, base, map[string]any{"platform": "testplat", "ref": "def"}); code != 409 || body["code"] != "platform_already_linked" {
		t.Fatalf("second account on a platform = %d %v", code, body)
	}
	bob := testutil.AddPlayer(t, svc, "1002")
	code, body, _ := call(t, h, http.MethodPost, "/api/v1/players/"+bob+"/identities", map[string]any{"platform": "testplat", "ref": "abc"})
	if code != 409 || body["code"] != "identity_linked_elsewhere" || body["player_id"] != alice {
		t.Fatalf("linked elsewhere = %d %v", code, body)
	}
	if code, _, _ := call(t, h, http.MethodPost, base, map[string]any{"platform": "nope", "ref": "abc"}); code != 422 {
		t.Fatalf("unknown platform = %d", code)
	}

	code, p, _ = call(t, h, http.MethodPatch, base+"/testplat", map[string]any{"enabled": false})
	if code != 200 || p["identities"].([]any)[1].(map[string]any)["enabled"] != false {
		t.Fatalf("pause = %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodPatch, base+"/nope", map[string]any{"enabled": true}); code != 422 {
		t.Fatalf("unknown platform = %d", code)
	}
	if code, _, _ := call(t, h, http.MethodDelete, base+"/testplat?delete_files=true", nil); code != 204 {
		t.Fatalf("unlink = %d", code)
	}
	if code, body, _ := call(t, h, http.MethodDelete, base+"/scoresaber", nil); code != 409 || body["code"] != "last_identity" {
		t.Fatalf("last account = %d %v", code, body)
	}
	if code, body, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": "1001"}); code != 409 || body["code"] != "identity_linked_elsewhere" {
		t.Fatalf("adding a tracked account = %d %v", code, body)
	}
}

func TestMergeEndpoint(t *testing.T) {
	svc, h := newMultiAPI(t, true)
	alice := seed(t, svc)
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	code, p, _ := call(t, h, http.MethodPost, "/api/v1/players/"+tess+"/merge", map[string]any{"into": alice})
	if code != 200 || p["id"] != alice || len(p["identities"].([]any)) != 2 {
		t.Fatalf("merge = %d %v", code, p)
	}
	if code, p, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess, nil); code != 200 || p["id"] != alice {
		t.Fatalf("a merged-away ID resolves to the survivor: %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess+"/scores", nil); code != 200 {
		t.Fatalf("alias scores = %d", code)
	}
	bob := testutil.AddPlayer(t, svc, "1002")
	if code, body, _ := call(t, h, http.MethodPost, "/api/v1/players/"+bob+"/merge", map[string]any{"into": alice}); code != 409 || body["code"] != "merge_platform_conflict" {
		t.Fatalf("conflict = %d %v", code, body)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/"+alice+"/merge", map[string]any{"into": alice}); code != 422 {
		t.Fatalf("self merge = %d", code)
	}
}

func TestPlatformScores(t *testing.T) {
	svc, h := newMultiAPI(t, false)
	alice := seed(t, svc)
	ctx := context.Background()
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0.Add(5*time.Minute), true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0.Add(4*time.Minute), false))
	testutil.Archive(t, svc, testutil.Row(t, svc, alice, "t1"), "tp")

	code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+alice+"/scores?platform=testplat", nil)
	items, _ := page["items"].([]any)
	if code != 200 || page["total"].(float64) != 2 {
		t.Fatalf("platform filter = %d %v", code, page)
	}
	t1 := items[0].(map[string]any)
	r := t1["replay"].(map[string]any)
	if t1["id"].(float64) != 0 || t1["platform"] != "testplat" || t1["external_id"] != "t1" ||
		t1["url"] != "https://replays.example.com/s/tp/t1" || r["download_url"] != "https://replays.example.com/r/tp/t1.tpr" ||
		r["embed_url"] != "https://replays.example.com/embed/tp/t1" ||
		t1["leaderboard"].(map[string]any)["id"].(float64) != 0 || t1["leaderboard"].(map[string]any)["external_id"] != "lb-a" {
		t.Fatalf("non-legacy score = %v", t1)
	}
	for q, want := range map[string]float64{"platform=all": 4, "min_score=960000": 2, "max_score=900": 2, "min_score=901&max_score=999999": 0} {
		code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+alice+"/scores?"+q, nil)
		if code != 200 || page["total"].(float64) != want {
			t.Errorf("%s = %d total %v, want %v", q, code, page["total"], want)
		}
	}
	for q, want := range map[string]int{"max_score=abc": 422, "min_score=-1": 422, "platform=nope": 422} {
		if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/"+alice+"/scores?"+q, nil); code != want {
			t.Errorf("%s = %d, want %d", q, code, want)
		}
	}

	if code, s, _ := call(t, h, http.MethodGet, "/api/v1/scores/testplat/t1", nil); code != 200 || s["external_id"] != "t1" {
		t.Fatalf("get-play = %d %v", code, s)
	}
	internal := testutil.Row(t, svc, alice, "t1").ID
	for _, p := range []string{"/api/v1/scores/testplat/attempt/t1", "/api/v1/scores/nope/t1", "/api/v1/scores/" + strconv.FormatInt(internal, 10)} {
		if code, _, _ := call(t, h, http.MethodGet, p, nil); code != 404 {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
}
