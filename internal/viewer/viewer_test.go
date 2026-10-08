package viewer_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/viewer"
)

func TestEmbeddedManifest(t *testing.T) {
	entries, err := viewer.ParseManifest(viewer.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("entries = %d, want 20", len(entries))
	}
	seen := map[string]string{}
	for _, e := range entries {
		seen[e.Path] = e.Commit
	}
	if seen["index.html"] != viewer.DeploySHA || seen["Build/ArcViewer.wasm"] != viewer.DeploySHA {
		t.Fatal("build files must be pinned to DeploySHA")
	}
	if seen["LICENSE"] != "a7b2d984f91346ade9f25e523afb5ed67cb2cb84" {
		t.Fatal("LICENSE must be pinned")
	}
}

func TestParseManifestRejects(t *testing.T) {
	bad := []string{
		"",
		"# only comments\n",
		"abc c776256497b66f7c91a74162cfcd943b0f45ee2e index.html",
		strings.Repeat("a", 64) + " short index.html",
		strings.Repeat("a", 64) + " c776256497b66f7c91a74162cfcd943b0f45ee2e ../etc/passwd",
		strings.Repeat("a", 64) + " c776256497b66f7c91a74162cfcd943b0f45ee2e",
	}
	for _, in := range bad {
		if _, err := viewer.ParseManifest(in); err == nil {
			t.Errorf("ParseManifest(%q) accepted", in)
		}
	}
}

func TestPlaceholderMatchesCommittedFile(t *testing.T) {
	b, err := fs.ReadFile(viewer.Bundle(), "PLACEHOLDER")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != viewer.PlaceholderText {
		t.Fatalf("PLACEHOLDER content %q != PlaceholderText", b)
	}
}
