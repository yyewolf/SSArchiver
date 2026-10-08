// Command ssarchiver archives and serves ScoreSaber replays.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // the scratch image has no zoneinfo

	_ "golang.org/x/crypto/x509roots/fallback" // the scratch image has no CA bundle

	"github.com/yyewolf/ssarchiver/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
