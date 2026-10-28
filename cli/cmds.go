// Package cli implements the ipmi-fan-controller command line interface: the cobra commands, the settings that are flags, and the server that
// serves the web page.
package cli

import (
	"fmt"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/spf13/cobra"
)

// name is what the program is called, on its pages and in its help.
const name = "ipmi-fan-controller"

func Make() (*cobra.Command, error) {
	return makeWith(live)
}

// makeWith is Make with what the program reaches outside itself with, so a test can stand in for each.
func makeWith(d deps) (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   name + " [command]",
		Short: name + " sets fan speeds through a server's or disk shelf's BMC from its disk and CPU temperatures",
		Long: `Reads a Linux machine's temperatures, its CPUs', its board's and its disks', and those its
BMCs report, and sets each fan zone on each BMC to the speed its curve asks for at them. A disk
shelf's fans follow only the disks in that shelf, found by its SAS address. It talks to the
machine's own BMC through /dev/ipmi0, and to any other over the network, a disk shelf's say.

It reads before it writes, never runs a zone under its floor, runs a zone fast when what it
follows stops answering, sets a zone again when its BMC has changed it, and when it stops puts
each zone back as it found it. --dry-run sets nothing and says what it would.
Complete documentation is available at https://github.com/katbyte/ipmi-fan-controller`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run \""+name+" help\" for more information about available "+name+" commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of " + name,
		Long:          `Print the version number of ` + name,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	})

	root.AddCommand(statusCmd(d))
	root.AddCommand(serveCmd(d))
	root.AddCommand(healthyCmd())

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}

// versionString is the name, version and commit the binary was built from, as the version command prints it and serve starts by saying.
// A build past a tag already names its commit in the version, so the commit is only added when it is not there.
func versionString() string {
	s := name + " " + version.Version
	if version.GitCommit != "" && !strings.Contains(version.Version, version.GitCommit) {
		s += " (" + version.GitCommit + ")"
	}

	return s
}
