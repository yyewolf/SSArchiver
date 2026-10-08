package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func newUserCmd(cfg *config.Config) *cobra.Command {
	user := &cobra.Command{Use: "user", Short: "Manage the admin account"}
	user.AddCommand(newResetPasswordCmd(cfg))
	return user
}

func readPassword(in io.Reader, prompt io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		_, _ = fmt.Fprint(prompt, "New password: ")
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(prompt)
		return string(b), err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func newResetPasswordCmd(cfg *config.Config) *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Set a new admin password (read from stdin) and log out all sessions",
		Example: "  echo 'new long password' | ssarchiver user reset-password\n" +
			"  docker exec -i ssarchiver /ssarchiver user reset-password < pw.txt",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			pw, err := readPassword(cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
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
			if err := service.New(gdb, nil, nil).ResetPassword(cmd.Context(), username, pw); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "password updated; existing sessions were logged out")
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "admin username (default: the only user)")
	return cmd
}
