package cli

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/makers"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/control"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// The defaults for the settings that are flags. The port, for the web page, is the one after kt's exporters'.
const (
	defaultListen = ":9126"
	defaultRoot   = "/"
)

// commandLimit is how long one command to a BMC over the network is given before it is sent again.
const commandLimit = 2 * time.Second

type FlagData struct {
	Config string `mapstructure:"config"`
	Listen string `mapstructure:"listen"`
	Web    bool   `mapstructure:"web"`
	Token  string `mapstructure:"web-token"`
	DryRun bool   `mapstructure:"dry-run"`
	Root   string `mapstructure:"root"`
}

// envNames binds each flag to its environment variable.
//
//nolint:gosec // G101: these are the names of the variables, the token's among them, not what they hold
var envNames = map[string]string{
	"config":    "IPMI_FAN_CONTROLLER_CONFIG",
	"listen":    "IPMI_FAN_CONTROLLER_LISTEN",
	"web":       "IPMI_FAN_CONTROLLER_WEB",
	"web-token": "IPMI_FAN_CONTROLLER_WEB_TOKEN",
	"dry-run":   "IPMI_FAN_CONTROLLER_DRY_RUN",
	"root":      "IPMI_FAN_CONTROLLER_ROOT",
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.String("config", "", "the settings file: ./ipmi-fan-controller.yaml, ~/.ipmi-fan-controller.yaml or /etc/ipmi-fan-controller.yaml unless named, the first that is there")
	pflags.String("listen", defaultListen, "serve the web page on this address, when it is on")
	pflags.Bool("web", false, "serve the web page, which shows everything and can change the settings and hold zones")
	pflags.String("web-token", "", "a token the web page needs before it changes anything")
	pflags.Bool("dry-run", false, "read everything and set nothing: each zone says what it would be set to")
	pflags.String("root", defaultRoot, "where the machine's own / is, for its /sys: /host say, for a container given it there")

	for name, env := range envNames {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if err := viper.BindEnv(name, env); err != nil {
			return fmt.Errorf("error binding '%s' to env '%s' : %w", name, env, err)
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData, read from viper so the environment is laid under the command line.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

// configName is the settings file's name, in the working directory or /etc; in the home directory it starts with a dot.
const configName = "ipmi-fan-controller.yaml"

// configPath is the settings file to read: the one named, or else the first there of ./ipmi-fan-controller.yaml, ~/.ipmi-fan-controller.yaml and
// /etc/ipmi-fan-controller.yaml. When none is there it is the one in the home directory, where the web page writes one. A leading ~/ is the home
// directory, for whatever starts the program without a shell to expand it.
func (f *FlagData) configPath() string {
	home, _ := os.UserHomeDir()
	if f.Config != "" {
		if rest, ok := strings.CutPrefix(f.Config, "~/"); ok && home != "" {
			return filepath.Join(home, rest)
		}

		return f.Config
	}

	inHome := filepath.Join(cmp.Or(home, "."), "."+configName)
	for _, p := range []string{configName, inHome, "/etc/" + configName} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return inHome
}

// deps is what the program reaches outside itself with, so a test can stand in for each.
type deps struct {
	// dial connects to a BMC.
	dial control.Dial
	// machine is the machine whose / is at root, asking disks with that smartctl.
	machine func(root, smartctl string) control.Machine
	// local says the machine has a BMC of its own that Linux's IPMI driver can reach.
	local func() bool
}

// live is the program's own: BMCs over the network or /dev/ipmi0, and the machine's /sys.
var live = deps{
	dial: func(ctx context.Context, b config.BMC) (bmc.Conn, error) {
		return bmc.Dial(ctx, bmc.Address{Host: b.Host, Port: b.Port, User: b.User, Password: b.Password, Timeout: commandLimit})
	},
	machine: func(root, smartctl string) control.Machine {
		return sensors.New(root, smartctl)
	},
	local: func() bool {
		for _, p := range []string{"/dev/ipmi0", "/dev/ipmi/0", "/dev/ipmidev/0"} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}

		return false
	},
}

// localName is what the machine's own BMC is called when the settings name no BMC and the program reads it anyway.
const localName = "local"

// parse checks a settings file's text and makes it what the controller runs on: with the machine's own BMC to read when the settings name none and
// it has one, so everything can be seen before anything is set up.
func (d deps) parse(text []byte) (*config.Config, error) {
	cfg, err := config.Parse(text, makers.Names())
	if err != nil {
		return nil, err
	}

	if len(cfg.BMCs) == 0 && d.local() {
		cfg.BMCs = append(cfg.BMCs, config.BMC{Name: localName, Maker: config.DefaultMaker, Port: config.DefaultPort, OnStop: config.OnStopRestore})
	}

	return cfg, nil
}

// settings reads the settings file and checks it: one that is not there is no BMCs and no zones, for reading only.
func (f *FlagData) settings(d deps) (*config.File, *config.Config, error) {
	path := f.configPath()
	file, err := config.Read(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the settings file %s (--config / %s): %w", path, envNames["config"], err)
	}

	cfg, err := d.parse(file.Text)
	if err != nil {
		return nil, nil, fmt.Errorf("the settings file %s:\n%w", path, err)
	}

	return file, cfg, nil
}
