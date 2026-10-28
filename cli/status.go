package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/katbyte/ipmi-fan-controller/lib/control"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
	"github.com/spf13/cobra"
)

func statusCmd(d deps) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Read everything once, set nothing, and print what there is and what each zone would be set to",
		Long: `Reads every temperature and every BMC once, as serve does each round, and prints them: the
sources and whether each answered, each BMC with its fans and mode, each zone with the speed it is
set to and the speed it would be set to and why, and every temperature with the zones that follow
it. It never sets anything, so it is the way to check the settings, and to find the names of the
sensors to follow before writing them.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			file, cfg, err := f.settings(d)
			if err != nil {
				return err
			}

			ctrl := control.New(control.Options{Config: cfg, Machine: d.machine(f.Root, sensors.Smartctl(cfg.Smartctl)), Dial: d.dial, DryRun: true, ReadAll: true})
			ctrl.Round(cmd.Context())
			ctrl.Stop(cmd.Context())
			st := ctrl.State()

			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")

				return enc.Encode(st)
			}

			if file.Exists {
				_, _ = fmt.Fprintf(out, "settings: %s\n", file.Path)
			} else {
				_, _ = fmt.Fprintf(out, "settings: none at %s, so nothing would be set\n", file.Path)
			}
			if err := printState(out, st); err != nil {
				return err
			}

			for _, b := range st.BMCs {
				if !b.Up {
					return fmt.Errorf("%s is not answering: %s", b.Name, b.Error)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print everything as JSON, as the web page reads it")

	return cmd
}

// printState prints a round as a person reads it.
func printState(out io.Writer, st control.State) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)

	_, _ = fmt.Fprintln(w, "\nSOURCE\tANSWERING\tUNREAD\tERROR")
	for _, s := range st.Sources {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", s.Name, yes(s.OK), len(s.Unread), s.Error)
	}

	for _, b := range st.BMCs {
		_, _ = fmt.Fprintf(w, "\nBMC %s\t%s\tmaker %s\tmode %s\tfirmware %s\t%s\n", b.Name, map[bool]string{true: "answering", false: "NOT ANSWERING"}[b.Up], b.Maker, dash(b.Mode), dash(b.Device.Firmware), b.Error)
		for _, f := range b.Fans {
			switch {
			case !f.Readable:
				_, _ = fmt.Fprintf(w, "  %s\tno reading\tzone %d\n", f.Name, f.Zone)
			case f.Stopped:
				_, _ = fmt.Fprintf(w, "  %s\tSTOPPED\tzone %d\n", f.Name, f.Zone)
			default:
				_, _ = fmt.Fprintf(w, "  %s\t%.0f rpm\tzone %d\n", f.Name, f.RPM, f.Zone)
			}
		}
	}

	_, _ = fmt.Fprintln(w, "\nZONE\tBMC\tSET NOW\tWOULD SET\tSTATE\tWHY")
	for _, z := range st.Zones {
		now := "-"
		if z.Duty != nil {
			now = fmt.Sprintf("%d%%", *z.Duty)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s/%d\t%s\t%d%%\t%s\t%s\n", z.Name, z.BMC, z.Zone, now, z.Want, z.State, strings.TrimPrefix(z.Why, fmt.Sprintf("would be set to %d%%: ", z.Want)))
	}
	if len(st.Zones) == 0 {
		_, _ = fmt.Fprintln(w, "none\t\t\t\t\t")
	}

	_, _ = fmt.Fprintln(w, "\nSENSOR\t°C\tFROM\tFOLLOWED BY\tDISK")
	for _, r := range st.Readings {
		disk := ""
		if r.Disk != nil {
			disk = strings.TrimSpace(r.Disk.Name + " " + r.Disk.Model)
		}
		_, _ = fmt.Fprintf(w, "%s\t%.1f\t%s\t%s\t%s\n", r.ID, r.Celsius, r.Source, strings.Join(r.Zones, ","), disk)
	}

	return w.Flush()
}

func yes(ok bool) string {
	if ok {
		return "yes"
	}

	return "NO"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}
