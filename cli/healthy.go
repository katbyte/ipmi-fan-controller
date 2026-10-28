package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// aliveName is the note serve leaves in the temp folder after each round, saying when the next round has to have happened by, which healthy
// reads. One program to a machine, or to a container, is assumed: a second would share the note.
const aliveName = "ipmi-fan-controller.alive"

// grace is how long past three intervals the next round may come: a round can take a while when a BMC is slow to answer.
const grace = 30 * time.Second

func alivePath() string {
	return filepath.Join(os.TempDir(), aliveName)
}

// alive notes that a round has just finished, and when the next has to have by: three intervals and the grace from now.
func alive(interval time.Duration) error {
	due := time.Now().Add(3*interval + grace).UTC().Format(time.RFC3339)

	tmp, err := os.CreateTemp(os.TempDir(), "."+aliveName+".*")
	if err != nil {
		return err
	}

	_, err = tmp.WriteString(due + "\n")
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), alivePath())
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}

	return err
}

// dead takes the note away when serve ends, so a stopped program is not taken for a live one.
func dead() {
	_ = os.Remove(alivePath())
}

func healthyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "healthy",
		Short: "Say whether serve is running and still setting the fans",
		Long: `Checks the note serve leaves after each round, and fails when there is none or the rounds have
stopped: the image's health check. It is healthy while the rounds go on, whatever state the BMCs
are in: a BMC that does not answer is in the log and on the web page.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			raw, err := os.ReadFile(alivePath())
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("serve is not running: it has left no note at %s", alivePath())
			}
			if err != nil {
				return err
			}

			due, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
			if err != nil {
				return fmt.Errorf("%s holds %q, which is not a time", alivePath(), strings.TrimSpace(string(raw)))
			}
			if time.Now().After(due) {
				return fmt.Errorf("serve's rounds have stopped: the next was due by %s", due.Local().Format(time.TimeOnly))
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "healthy: the next round is due by %s\n", due.Local().Format(time.TimeOnly))

			return nil
		},
	}
}
