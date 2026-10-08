package cli

import (
	"log/slog"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/app"
	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/config"
)

func setupLogging(level string) {
	lvl, _ := config.ParseLogLevel(level)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}

func newServeCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the web server and the archiver",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			setupLogging(cfg.LogLevel)
			a, err := app.New(*cfg, app.Options{})
			if err != nil {
				return err
			}
			defer func() { _ = a.Close() }()
			var lc net.ListenConfig
			ln, err := lc.Listen(cmd.Context(), "tcp", cfg.Listen)
			if err != nil {
				return err
			}
			slog.Info("ssarchiver started", "addr", ln.Addr().String(), "data", cfg.DataDir, "version", buildinfo.Version)
			return a.Serve(cmd.Context(), ln)
		},
	}
}
