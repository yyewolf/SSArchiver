// Package cli defines the ssarchiver command tree.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/config"
)

// NewRootCmd builds the command tree. Subcommands share one *config.Config.
func NewRootCmd() *cobra.Command {
	cfg := config.FromEnv()
	root := &cobra.Command{
		Use:           "ssarchiver",
		Short:         "Archive and serve ScoreSaber replays",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cfg.BindFlags(root.PersistentFlags())
	root.AddCommand(newVersionCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context) int {
	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
