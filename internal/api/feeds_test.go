package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func newFakeAPI(t *testing.T, admin bool) (*service.Service, *testutil.FakePlatform, http.Handler) {
	t.Helper()
	svc, fp, _ := testutil.NewMultiService(t)
	return svc, fp, apiHandler(svc, admin)
}

func feedOf(t *testing.T, player map[string]any, platformName, kind string) map[string]any {
	t.Helper()
	for _, id := range player["identities"].([]any) {
		idm := id.(map[string]any)
		if idm["platform"] != platformName {
			continue
		}
		for _, f := range idm["feeds"].([]any) {
			if fm := f.(map[string]any); fm["kind"] == kind {
				return fm
			}
		}
	}
	t.Fatalf("no %s %s feed in %v", platformName, kind, player)
	return nil
}

func TestRateLimitedFeedCheck(t *testing.T) {
	svc, fp, h := newFakeAPI(t, true)
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	base := "/api/v1/players/" + tess + "/identities/testplat/feeds/"

	fp.ProbeErr = fmt.Errorf("%w: slow down", platform.ErrRateLimited)
	if code, _, _ := call(t, h, http.MethodPatch, base+"attempt", map[string]any{"enabled": true}); code != 429 {
		t.Fatalf("rate-limited enable = %d, want 429", code)
	}
	if code, _, _ := call(t, h, http.MethodPost, base+"attempt/check", nil); code != 429 {
		t.Fatalf("rate-limited check = %d, want 429", code)
	}
	fp.ProbeErr = nil
	_, p, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess, nil)
	if af := feedOf(t, p, "testplat", "attempt"); af["access"] != "unknown" || af["access_checked_at"] != nil {
		t.Fatalf("the switch created the row, the throttled probe recorded nothing: %v", af)
	}

	fp.Access["attempt/abc"] = model.AccessPrivate
	code, f, _ := call(t, h, http.MethodPost, base+"attempt/check", nil)
	hint, _ := f["hint"].(map[string]any)
	if code != 200 || f["access"] != "private" || hint["title"] != "History is private" {
		t.Fatalf("check after throttling = %d %v", code, f)
	}
}

func TestFeedEndpoints(t *testing.T) {
	svc, fp, h := newFakeAPI(t, true)
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	base := "/api/v1/players/" + tess + "/identities/testplat/feeds/"

	fp.Access["attempt/abc"] = model.AccessPrivate
	code, f, _ := call(t, h, http.MethodPatch, base+"attempt", map[string]any{"enabled": true})
	hint, _ := f["hint"].(map[string]any)
	if code != 200 || f["kind"] != "attempt" || f["optional"] != true || f["enabled"] != true || f["access"] != "private" ||
		f["access_checked_at"] == nil || hint["title"] != "History is private" || len(hint["steps"].([]any)) != 2 ||
		hint["link_url"] != "https://tp.example/settings" {
		t.Fatalf("switch on, private = %d %v", code, f)
	}

	fp.Access["attempt/abc"] = model.AccessPublic
	fp.AccessTotal = 7
	code, f, _ = call(t, h, http.MethodPost, base+"attempt/check", nil)
	if code != 200 || f["access"] != "public" || f["remote_total"].(float64) != 7 || f["hint"] != nil {
		t.Fatalf("check again = %d %v", code, f)
	}

	_, p, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess, nil)
	if af := feedOf(t, p, "testplat", "attempt"); af["optional"] != true || af["counts"] == nil {
		t.Fatalf("player DTO feed = %v", af)
	}
	if sf := feedOf(t, p, "testplat", "score"); sf["optional"] != false || sf["access"] != "n/a" {
		t.Fatalf("required feed = %v", sf)
	}

	if code, _, _ := call(t, h, http.MethodPatch, base+"score", map[string]any{"enabled": false}); code != 422 {
		t.Fatalf("required feeds cannot be switched: %d", code)
	}
	if code, _, _ := call(t, h, http.MethodPatch, "/api/v1/players/"+tess+"/identities/scoresaber/feeds/attempt", map[string]any{"enabled": true}); code != 404 {
		t.Fatalf("no such feed: %d", code)
	}
	if code, _, _ := call(t, h, http.MethodPatch, base+"bogus", map[string]any{"enabled": true}); code != 422 {
		t.Fatalf("kind is an enum: %d", code)
	}
	code, f, _ = call(t, h, http.MethodPatch, base+"attempt", map[string]any{"enabled": false})
	if code != 200 || f["enabled"] != false || f["access"] != "public" {
		t.Fatalf("switch off = %d %v", code, f)
	}
	alice := testutil.AddPlayer(t, svc, "1001")
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/"+alice+"/identities/testplat/feeds/attempt/check", nil); code != 404 {
		t.Fatalf("check without an account: %d", code)
	}
}
