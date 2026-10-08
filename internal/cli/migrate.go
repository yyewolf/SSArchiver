package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
)

func newMigrateCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Create or update the database schema and exit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			gdb, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer func() { _ = db.Close(gdb) }()
			if err := db.Migrate(gdb); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "migrations applied:", cfg.DBPath())
			return nil
		},
	}
}
