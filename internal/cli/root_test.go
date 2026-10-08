package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/cli"
)

func TestVersionCommand(t *testing.T) {
	root := cli.NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ssarchiver dev") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}
