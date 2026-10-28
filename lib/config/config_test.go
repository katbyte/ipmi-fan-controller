package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var makers = []string{"supermicro", "dell"}

// lab is the settings for lab: the server's own BMC, read and not set, and the disk shelf's, over the network, with zone 0 following the disks
// in the shelf's front.
const lab = `# lab: tower and its disk shelf
interval: 15s
bmcs:
  - name: tower
  - name: annex
    host: annex-ipmi.example.net
    user: ADMIN
    password: ${TEST_ANNEX_PASSWORD}  # from the environment
zones:
  - name: shelf
    bmc: annex
    zone: 0
    floor: 35          # the stock fans' quietest that still moves air
    follow:
      - name: disks
        sensors: [disk/5003048000f8a93f/*]
        curve: {35: 35, 45: 50, 52: 100}
      - sensors: ["ipmi/annex/SAS3 * Temp"]
        by: average
        curve: {70: 35, 90: 100}
`

// Every setting the file leaves out is its default, and each ${NAME} is what the environment has.
func TestSettingsWithTheirDefaults(t *testing.T) {
	t.Setenv("TEST_ANNEX_PASSWORD", "hunter2")

	c, err := Parse([]byte(lab), makers)
	if err != nil {
		t.Fatal(err)
	}

	if time.Duration(c.Interval) != 15*time.Second || time.Duration(c.DiskInterval) != DefaultDiskInterval || c.Hysteresis != DefaultHysteresis || c.Smartctl != SmartctlAuto {
		t.Errorf("the top level is %+v", c)
	}

	tower, annex := c.BMCs[0], c.BMCs[1]
	if !tower.Local() || tower.Maker != DefaultMaker || tower.OnStop != OnStopRestore {
		t.Errorf("tower is %+v, want the machine's own Supermicro BMC, put back on stop", tower)
	}
	if annex.Local() || annex.Port != DefaultPort || annex.Password != "hunter2" {
		t.Errorf("annex is %+v, want one on the network on 623 with the password from the environment", annex)
	}

	z, ok := c.Zone("shelf")
	if !ok || z.Floor != 35 || z.Ceiling != DefaultCeiling || z.Failsafe != DefaultFailsafe || len(z.Follow) != 2 {
		t.Fatalf("shelf is %+v", z)
	}
	if z.Follow[0].By != ByHottest || z.Follow[1].By != ByAverage || z.Follow[1].Label() != "ipmi/annex/SAS3 * Temp" {
		t.Errorf("the follows are %+v", z.Follow)
	}
	if !z.Follow[0].Matches("disk/5003048000f8a93f/ZR10A001") || z.Follow[0].Matches("disk/5003048000c4d53f/ZR10A001") || !z.Follow[1].Matches("ipmi/annex/SAS3 F1 Temp") {
		t.Error("the follows match the wrong sensors")
	}
}

// A file that is empty, or not there, is no BMCs and no zones.
func TestNothingAtAll(t *testing.T) {
	t.Parallel()

	c, err := Parse(nil, makers)
	if err != nil || len(c.BMCs) != 0 || len(c.Zones) != 0 || time.Duration(c.Interval) != DefaultInterval {
		t.Errorf("got %+v (%v)", c, err)
	}
}

// A curve is straight lines between its points, flat beyond its ends.
func TestACurve(t *testing.T) {
	t.Parallel()

	c := Curve{{Celsius: 35, Duty: 30}, {Celsius: 45, Duty: 50}, {Celsius: 55, Duty: 100}}
	for celsius, want := range map[float64]float64{20: 30, 35: 30, 40: 40, 45: 50, 50: 75, 55: 100, 70: 100} {
		if got := c.At(celsius); got != want {
			t.Errorf("at %g° the curve asks for %g, want %g", celsius, got, want)
		}
	}
	if c.Top() != 55 {
		t.Errorf("the top is %g", c.Top())
	}
}

// Every mistake is named at once, each with where it is, and a mistyped setting is one: it would otherwise be quietly left at its default.
func TestMistakes(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`
intervl: 10s
bmcs:
  - name: tower
  - name: tower
    maker: acme
    host: 10.0.0.1
  - name: other
zones:
  - name: shelf
    bmc: annex
    zone: 0
    flor: 20
    follow: []
  - name: cpu
    bmc: tower
    zone: 0
    floor: 50
    ceiling: 40
    follow:
      - sensors: ["hwmon/[k10temp/Tctl"]
        curve: {40: 80, 70: 30}
`), makers)
	if err == nil {
		t.Fatal("the mistakes were taken")
	}

	for _, want := range []string{"intervl is not a setting", "zones[0].flor is not a setting"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}

	// once every setting is known, what they say is checked
	_, err = Parse([]byte(`
bmcs:
  - name: tower
  - name: tower
    maker: acme
    host: 10.0.0.1
  - name: other
zones:
  - name: shelf
    bmc: annex
    zone: 0
    follow: []
  - name: cpu
    bmc: tower
    zone: 0
    floor: 50
    ceiling: 40
    follow:
      - sensors: ["hwmon/[k10temp/Tctl"]
        curve: {40: 80, 70: 30}
`), makers)
	if err == nil {
		t.Fatal("the mistakes were taken")
	}

	for _, want := range []string{
		"two BMCs are called tower",
		`"acme" is not one this program knows: supermicro, dell`,
		"a BMC on the network needs a login",
		"a BMC on the network needs a password",
		"tower and other both have no host",
		`zones[0] (shelf).bmc: "annex" is not one of the bmcs`,
		"a zone has to follow something",
		"floor (50) and ceiling (40)",
		"is not a pattern",
		"a curve has to rise or stay level",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
}

// A password from the environment that is not there is a mistake, not an empty password.
func TestAPasswordNotThere(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte("bmcs:\n  - name: annex\n    host: x\n    user: ADMIN\n    password: ${TEST_NOT_SET_ANYWHERE}\n"), makers)
	if err == nil || !strings.Contains(err.Error(), "the environment has no TEST_NOT_SET_ANYWHERE") {
		t.Errorf("got %v", err)
	}
}

// A password can be read from a file, a container's secret say, less the line's end.
func TestAPasswordFromAFile(t *testing.T) {
	t.Parallel()

	secret := filepath.Join(t.TempDir(), "annex")
	if err := os.WriteFile(secret, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Parse([]byte("bmcs:\n  - name: annex\n    host: x\n    user: ADMIN\n    password_file: "+secret+"\n"), makers)
	if err != nil || c.BMCs[0].Password != "hunter2" {
		t.Errorf("got %+v (%v)", c, err)
	}
}

// A zone changed on the web page is saved with everything else in the file as it was, comments too, and reads back as changed.
func TestEditingAZone(t *testing.T) {
	t.Setenv("TEST_ANNEX_PASSWORD", "hunter2")

	floor := 40
	text, err := EditZone([]byte(lab), "shelf", ZoneEdit{Floor: &floor, Curves: map[int]Curve{0: {{Celsius: 38, Duty: 40}, {Celsius: 50.5, Duty: 100}}}})
	if err != nil {
		t.Fatal(err)
	}

	for _, kept := range []string{"# lab: tower and its disk shelf", "# from the environment", "# the stock fans' quietest that still moves air", "${TEST_ANNEX_PASSWORD}", "{70: 35, 90: 100}"} {
		if !strings.Contains(string(text), kept) {
			t.Errorf("the edit lost %q:\n%s", kept, text)
		}
	}

	c, err := Parse(text, makers)
	if err != nil {
		t.Fatalf("the edited file does not read: %v\n%s", err, text)
	}

	z, _ := c.Zone("shelf")
	if z.Floor != 40 || len(z.Follow[0].Curve) != 2 || z.Follow[0].Curve[1].Celsius != 50.5 || z.Follow[1].Curve[0].Celsius != 70 {
		t.Errorf("the edited zone is %+v", z)
	}

	if _, err := EditZone([]byte(lab), "attic", ZoneEdit{Floor: &floor}); err == nil {
		t.Error("a zone that is not there was edited")
	}
}

// An enclosure assigned to a zone on the web page joins the zone's follow of disks, and is followed by the same curve; a zone with none gets one by
// DiskCurve. Only disks are added this way, and only once.
func TestFollowingMoreDisks(t *testing.T) {
	t.Setenv("TEST_ANNEX_PASSWORD", "hunter2")

	text, err := EditZone([]byte(lab), "shelf", ZoneEdit{Follow: "disk/5003048000a1b23f/*"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(text, makers)
	if err != nil {
		t.Fatalf("the edited file does not read: %v\n%s", err, text)
	}
	z, _ := c.Zone("shelf")
	if len(z.Follow) != 2 || len(z.Follow[0].Sensors) != 2 || z.Follow[0].Sensors[1] != "disk/5003048000a1b23f/*" || !strings.Contains(string(text), "# the stock fans' quietest that still moves air") {
		t.Errorf("the shelf follows %+v:\n%s", z.Follow, text)
	}

	if _, err := EditZone(text, "shelf", ZoneEdit{Follow: "disk/5003048000a1b23f/*"}); err == nil || !strings.Contains(err.Error(), "already follows") {
		t.Errorf("following the same disks twice: %v", err)
	}
	if _, err := EditZone(text, "shelf", ZoneEdit{Follow: "hwmon/*/*"}); err == nil {
		t.Error("a chip was added as if it were disks")
	}

	cpu := lab + `  - name: cpu
    bmc: tower
    zone: 0
    follow:
      - sensors: [hwmon/k10temp/Tctl]
        curve: {40: 30, 80: 100}
`
	text, err = EditZone([]byte(cpu), "cpu", ZoneEdit{Follow: "disk/5003048000c4d53f/*"})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = Parse(text, makers); err != nil {
		t.Fatalf("the edited file does not read: %v\n%s", err, text)
	}
	z, _ = c.Zone("cpu")
	if len(z.Follow) != 2 || z.Follow[1].Name != "disks" || len(z.Follow[1].Curve) != len(DiskCurve) || z.Follow[1].Sensors[0] != "disk/5003048000c4d53f/*" {
		t.Errorf("the cpu zone follows %+v:\n%s", z.Follow, text)
	}
}

// A curve of its own added on the web page is followed beside the zone's others, and one taken away is gone, numbered as the follows were before
// the change.
func TestCurvesOfTheirOwn(t *testing.T) {
	t.Setenv("TEST_ANNEX_PASSWORD", "hunter2")

	edit := ZoneEdit{
		Curves: map[int]Curve{0: {{Celsius: 40, Duty: 30}, {Celsius: 55, Duty: 100}}},
		Drop:   []int{1, 1},
		Add:    []NewFollow{{Name: "inside the shelf", Sensors: []string{"ipmi/annex/System Temp"}, Curve: Curve{{Celsius: 30, Duty: 30}, {Celsius: 45, Duty: 80}}}},
	}
	text, err := EditZone([]byte(lab), "shelf", edit)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(text, makers)
	if err != nil {
		t.Fatalf("the edited file does not read: %v\n%s", err, text)
	}

	z, _ := c.Zone("shelf")
	if len(z.Follow) != 2 || z.Follow[0].Curve[1].Celsius != 55 || z.Follow[1].Name != "inside the shelf" || z.Follow[1].Sensors[0] != "ipmi/annex/System Temp" || z.Follow[1].Curve[1].Duty != 80 {
		t.Errorf("the shelf follows %+v:\n%s", z.Follow, text)
	}
	if strings.Contains(string(text), "{70: 35, 90: 100}") || !strings.Contains(string(text), "# the stock fans' quietest that still moves air") {
		t.Errorf("the follow taken away is still there, or a comment went with it:\n%s", text)
	}

	if _, err := EditZone([]byte(lab), "shelf", ZoneEdit{Drop: []int{2}}); err == nil {
		t.Error("a follow that is not there was taken away")
	}
}

// Saving writes the whole file, keeping who may read it, and a file that was not there is written.
func TestSaving(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ipmi-fan-controller.yaml")
	f, err := Read(path)
	if err != nil || f.Exists {
		t.Fatalf("a file not there reads as %+v (%v)", f, err)
	}

	if err := f.Save([]byte("interval: 5s\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := f.Save([]byte("interval: 6s\n")); err != nil {
		t.Fatal(err)
	}

	again, err := Read(path)
	if err != nil || !again.Exists || string(again.Text) != "interval: 6s\n" {
		t.Errorf("read back %+v (%v)", again, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o400 {
		t.Errorf("the file's mode is %v (%v), want it kept at 0400", info.Mode().Perm(), err)
	}
}
