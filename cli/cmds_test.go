package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/bmctest"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/control"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors/sensorstest"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// alone keeps a test's settings to itself: viper's own, the environment's, and the settings files in its own home and working directory.
func alone(t *testing.T) (home, wd string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	home, wd = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	t.Chdir(wd)
	for _, env := range envNames {
		t.Setenv(env, "")
	}

	return home, wd
}

// lab is a machine laid out as a real one is, with one disk in the shelf's front at 50° and one in the server's front at 58°, and its shelf's BMC.
func lab(t *testing.T) (string, *bmctest.Board) {
	t.Helper()

	tr := sensorstest.New(t)
	tr.Chip("k10temp", sensorstest.Temp{Label: "Tctl", Celsius: 54.5})
	shelf := tr.Enclosure("8:0:24:0", "0x5003048000f8a93f")
	server := tr.Enclosure("1:0:24:0", "0x5003048000c4d53f")
	tr.Disk(sensorstest.Disk{Name: "sdbc", Serial: "ZR10A001", Model: "WUH722020BL5204", Celsius: new(50.0), Enclosure: shelf, Slot: 1})
	tr.Disk(sensorstest.Disk{Name: "sdf", Serial: "ZR20B001", Model: "WUH722020BL5204", Celsius: new(58.0), Enclosure: server, Slot: 4})

	board := bmctest.Shelf(60).Sensor("SAS3 F1 Die Temp", bmc.Temperature, 68).Sensor("FAN1", bmc.Fan, 4500).Unreadable("FAN8", bmc.Fan)

	return tr.Root, board
}

const settings = `# lab's shelf
interval: 1s
disk_interval: 1s
bmcs:
  - name: annex
    host: 192.0.2.10
    user: ADMIN
    password: x
zones:
  - name: shelf
    bmc: annex
    zone: 0
    follow:
      - name: disks
        sensors: [disk/5003048000f8a93f/*]
        curve: {35: 30, 45: 50, 55: 100}
`

// fakes is the program reaching a machine whose / is at root, these boards in place of BMCs, and a BMC of its own when local says.
func fakes(boards bmctest.Boards, local bool) deps {
	return deps{
		dial: func(ctx context.Context, b config.BMC) (bmc.Conn, error) { return boards.Dial(ctx, b.Name) },
		machine: func(root, smartctl string) control.Machine {
			return sensors.New(root, smartctl)
		},
		local: func() bool { return local },
	}
}

// program is the program given these arguments, not yet run, and where what it prints will go.
func program(t *testing.T, d deps, args ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()

	root, err := makeWith(d)
	if err != nil {
		t.Fatalf("makeWith: %v", err)
	}

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)

	return root, &out
}

func run(t *testing.T, d deps, args ...string) (string, error) {
	t.Helper()

	root, out := program(t, d, args...)
	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

func write(t *testing.T, path, text string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestMakeBuildsTheCommands(t *testing.T) {
	alone(t)

	root, err := Make()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"version", "status", "serve", "healthy"} {
		if c, _, err := root.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("no %s command: %v", name, err)
		}
	}
	for name := range envNames {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("no --%s flag", name)
		}
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestVersion(t *testing.T) {
	alone(t)

	out, err := run(t, fakes(nil, false), "version")
	if err != nil || !strings.HasPrefix(out, "ipmi-fan-controller "+version.Version) {
		t.Errorf("version printed %q (%v)", out, err)
	}
}

// status reads the machine and its shelf once and says what it would set, without setting anything.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestStatus(t *testing.T) {
	_, wd := alone(t)

	root, board := lab(t)
	write(t, filepath.Join(wd, configName), settings)

	out, err := run(t, fakes(bmctest.Boards{"annex": board}, false), "status", "--root", root)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}

	for _, want := range []string{
		"settings: " + configName,
		"BMC annex",
		"FAN1",
		"no reading",
		"shelf  annex/0  60%      75%        auto   disks at 50.0° asks for 75%",
		"disk/5003048000f8a93f/ZR10A001",
		"hwmon/k10temp/Tctl",
		"ipmi/annex/SAS3 F1 Die Temp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status does not say %q:\n%s", want, out)
		}
	}
	if len(board.Writes()) != 0 {
		t.Errorf("status set %v", board.Writes())
	}

	out, err = run(t, fakes(bmctest.Boards{"annex": board}, false), "status", "--root", root, "--json")
	var st control.State
	if err != nil || json.Unmarshal([]byte(out), &st) != nil || len(st.Zones) != 1 || st.Zones[0].Want != 75 {
		t.Errorf("status --json printed %.300s (%v)", out, err)
	}
}

// Settings with mistakes in them stop the program before it touches anything, naming the file and every mistake.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestMistakesInTheSettings(t *testing.T) {
	_, wd := alone(t)

	_, board := lab(t)
	path := filepath.Join(wd, "fans.yaml")
	write(t, path, strings.Replace(settings, "    zone: 0\n", "    zone: 0\n    flor: 20\n", 1))

	_, err := run(t, fakes(bmctest.Boards{"annex": board}, false), "status", "--config", path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "zones[0].flor is not a setting") {
		t.Errorf("got %v", err)
	}
}

// With no settings file, the machine's own BMC is read, so everything there is to follow can be seen before anything is set up.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestNoSettings(t *testing.T) {
	alone(t)

	root, _ := lab(t)
	own := bmctest.Mainboard(50).Sensor("CPU Temp", bmc.Temperature, 55)

	out, err := run(t, fakes(bmctest.Boards{localName: own}, true), "status", "--root", root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "settings: none at") || !strings.Contains(out, "BMC local") || !strings.Contains(out, "ipmi/local/CPU Temp") {
		t.Errorf("status printed:\n%s", out)
	}
}

// The settings file is the one named, or the first there of the working directory's, the home directory's and /etc's; with none it is the home
// directory's, where the web page writes one.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestWhichSettingsFile(t *testing.T) {
	home, wd := alone(t)

	inHome := filepath.Join(home, "."+configName)
	if got := (&FlagData{}).configPath(); got != inHome && !strings.HasPrefix(got, "/etc/") {
		t.Errorf("with none it is %s, want %s", got, inHome)
	}

	write(t, inHome, "")
	if got := (&FlagData{}).configPath(); got != inHome {
		t.Errorf("with one at home it is %s", got)
	}

	write(t, filepath.Join(wd, configName), "")
	if got := (&FlagData{}).configPath(); got != configName {
		t.Errorf("with one here it is %s", got)
	}

	if got := (&FlagData{Config: "~/fans.yaml"}).configPath(); got != filepath.Join(home, "fans.yaml") {
		t.Errorf("named ~/fans.yaml it is %s", got)
	}
}

// freePort is an address on the loopback nothing is listening on.
func freePort(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	return addr
}

// get fetches a page, and gives back what it said, or "" when it could not be fetched.
func get(t *testing.T, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = res.Body.Close() }()

	raw, _ := io.ReadAll(res.Body)

	return string(raw)
}

// until waits up to ten seconds for check to pass, and says whether it did.
func until(check func() bool) bool {
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}

	return true
}

// healthy runs the healthy command, and gives back what it said.
func healthy(t *testing.T) (string, error) {
	t.Helper()

	cmd := healthyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(t.Context())

	return out.String(), err
}

// serve with the web page sets the shelf from its disks, serves the page and its state, takes up a curve changed on the page, and when stopped
// puts the shelf back at the 60% it found it at.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestServe(t *testing.T) {
	_, wd := alone(t)

	root, board := lab(t)
	write(t, filepath.Join(wd, configName), settings)
	addr := freePort(t)

	cmd, _ := program(t, fakes(bmctest.Boards{"annex": board}, false), "serve", "--root", root, "--listen", addr, "--web", "--web-token", "sesame")
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.ExecuteContext(ctx) }()

	shown := func(duty int) bool {
		var st control.State
		if json.Unmarshal([]byte(get(t, "http://"+addr+"/api/state")), &st) != nil || len(st.Zones) != 1 || st.Zones[0].Duty == nil {
			return false
		}

		return *st.Zones[0].Duty == duty
	}
	if !until(func() bool { return shown(75) }) {
		t.Fatalf("the page never showed the shelf at 75%%; it is at %d%%", board.Duty(0))
	}
	if body := get(t, "http://"+addr+"/"); !strings.Contains(body, "<title>ipmi-fan-controller</title>") {
		t.Errorf("the web page is %.200s", body)
	}
	if body := get(t, "http://"+addr+"/metrics"); strings.Contains(body, "ipmi_fan_controller") {
		t.Error("the program publishes metrics")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, "http://"+addr+"/api/zones/shelf", strings.NewReader(`{"curves": {"0": [{"celsius": 40, "duty": 30}, {"celsius": 60, "duty": 100}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sesame")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("changing the curve answered %d", res.StatusCode)
	}

	if !until(func() bool { return shown(65) && board.Duty(0) == 65 }) {
		t.Errorf("the new curve was never taken up: the shelf is at %d%%", board.Duty(0))
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("serve stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop when told to")
	}

	if board.Duty(0) != 60 {
		t.Errorf("the shelf was left at %d%%, want the 60%% it was found at", board.Duty(0))
	}
}

// serve without the web page opens no port, so it runs though its address is taken; it sets the shelf, reading only what the shelf follows, and is
// healthy while it runs and not once it has stopped.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestServeWithoutTheWebPage(t *testing.T) {
	_, wd := alone(t)

	root, board := lab(t)
	write(t, filepath.Join(wd, configName), settings)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	if _, err := healthy(t); err == nil {
		t.Error("healthy before serve has run")
	}

	cmd, _ := program(t, fakes(bmctest.Boards{"annex": board}, false), "serve", "--root", root, "--listen", ln.Addr().String())
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.ExecuteContext(ctx) }()

	if !until(func() bool { return board.Duty(0) == 75 }) {
		t.Fatalf("the shelf was never set to 75%%; it is at %d%%", board.Duty(0))
	}
	if !until(func() bool { _, err := healthy(t); return err == nil }) {
		_, err := healthy(t)
		t.Errorf("serve is running and healthy says %v", err)
	}
	if board.SensorReads() != 0 {
		t.Errorf("the shelf's sensors were read %d times, and no zone follows them", board.SensorReads())
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("serve stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop when told to")
	}

	if board.Duty(0) != 60 {
		t.Errorf("the shelf was left at %d%%, want the 60%% it was found at", board.Duty(0))
	}
	if _, err := healthy(t); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("serve has stopped and healthy says %v", err)
	}
}

// A note left by rounds that have stopped is not healthy.
//
//nolint:paralleltest // the note's folder comes from the environment
func TestHealthyWhenTheRoundsStop(t *testing.T) {
	alone(t)

	write(t, alivePath(), time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)+"\n")
	if _, err := healthy(t); err == nil || !strings.Contains(err.Error(), "rounds have stopped") {
		t.Errorf("healthy says %v", err)
	}

	if err := alive(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if out, err := healthy(t); err != nil || !strings.Contains(out, "healthy") {
		t.Errorf("healthy says %q (%v)", out, err)
	}
}

// With the web page on, a port that is already taken stops the program before it touches any BMC, with the address in the error.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestServeSaysWhenThePortIsTaken(t *testing.T) {
	_, wd := alone(t)

	root, board := lab(t)
	write(t, filepath.Join(wd, configName), settings)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	_, err = run(t, fakes(bmctest.Boards{"annex": board}, false), "serve", "--root", root, "--listen", addr, "--web")
	if err == nil || !strings.Contains(err.Error(), addr) {
		t.Errorf("serve on a taken port gave %v, want an error naming %s", err, addr)
	}
	if len(board.Writes()) != 0 {
		t.Errorf("the shelf was set: %v", board.Writes())
	}
}
