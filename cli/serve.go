package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/httpd"
	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/ipmi-fan-controller/lib/control"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
	"github.com/katbyte/ipmi-fan-controller/lib/web"
	"github.com/spf13/cobra"
)

func serveCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Set the fans, and serve the web page when it is on",
		Long: `Reads the temperatures every interval (10s), the disks' every disk_interval (1m), and sets each
zone to the speed its curves ask for. It publishes no metrics: fan speeds and the rest are for an
IPMI exporter to read.

With --web it serves the web page at / on --listen (:9126): every zone, fan and temperature, the
curves to change, the settings file to edit, and holds to run a zone quiet or flat out for a
while. Only then does it read the temperatures, fans and fan modes no zone follows, for the page
to show. Without it the program reads only what the zones follow, and opens no port at all.

Everything else is in the settings file, YAML: the BMCs, the zones on them and what each
follows. kill -HUP reads it again. With no settings file it sets nothing.

When it stops it puts each zone back at the speed it was found at, and a Supermicro board back
in the fan mode it was in; on_stop: full leaves a BMC's zones at full speed instead. If it is
killed outright, or its machine stops, the fans stay where they were last set.

After each round it notes that it is alive, which "ipmi-fan-controller healthy" checks: the
image's health check.

Every flag is also an environment variable: IPMI_FAN_CONTROLLER_CONFIG, IPMI_FAN_CONTROLLER_LISTEN,
IPMI_FAN_CONTROLLER_WEB, IPMI_FAN_CONTROLLER_WEB_TOKEN, IPMI_FAN_CONTROLLER_DRY_RUN and
IPMI_FAN_CONTROLLER_ROOT.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			// said before anything else, so a log shows which build it came from
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), versionString())

			f := GetFlags()
			file, cfg, err := f.settings(d)
			if err != nil {
				return err
			}
			if !file.Exists {
				clog.Log.Warnf("there is no settings file at %s, so nothing is set", file.Path)
			}
			if f.DryRun {
				clog.Log.Warn("a dry run: nothing is set, and each zone says what it would be set to")
			}

			// one signal stops the setting and the serving; the fans are handed back before the program ends
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// the address is taken before any BMC is touched, so one already in use stops the program with nothing changed
			var ln net.Listener
			if f.Web {
				if f.Token == "" {
					clog.Log.Warnf("the web page is on with no token: anyone who can reach %s can change the fans (%s)", f.Listen, envNames["web-token"])
				}

				if ln, err = httpd.Listen(ctx, f.Listen); err != nil {
					return err
				}
				clog.Log.Infof("serving the web page on %s", ln.Addr())
			}

			ctrl := control.New(control.Options{
				Config:  cfg,
				Machine: d.machine(f.Root, sensors.Smartctl(cfg.Smartctl)),
				Dial:    d.dial,
				DryRun:  f.DryRun,
				ReadAll: f.Web,
				After: func(st control.State) {
					if err := alive(st.Interval); err != nil {
						clog.Log.Warnf("noting that the program is alive: %v", err)
					}
				},
			})
			defer dead()

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			var wg sync.WaitGroup
			wg.Go(func() { ctrl.Run(ctx) })
			wg.Go(func() { reloads(ctx, f, d, ctrl) })

			if ln != nil {
				err = httpd.Serve(ctx, ln, web.Handler(web.Options{Controller: ctrl, Path: file.Path, Parse: d.parse, Token: f.Token, Name: name, Version: version.Version}))
				cancel()
			} else {
				<-ctx.Done()
			}
			wg.Wait()

			return err
		},
	}
}

// reloads reads the settings file again each time the program is sent SIGHUP. Settings with anything wrong in them are not taken up: the ones in use
// are kept, and what is wrong is logged.
func reloads(ctx context.Context, f *FlagData, d deps, ctrl *control.Controller) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
		}

		file, cfg, err := f.settings(d)
		if err != nil {
			clog.Log.Errorf("kept the settings in use: %v", err)
			continue
		}

		ctrl.Apply(cfg)
		clog.Log.Infof("read %s again, and took it up", file.Path)
	}
}
