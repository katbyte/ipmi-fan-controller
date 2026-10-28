package sensors_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors/sensorstest"
)

// shelf and server are lab's shelf front and server front, by the SAS addresses sas-exporter gives them.
const (
	shelf  = "0x5003048000f8a93f"
	server = "0x5003048000c4d53f"
)

// lab is a machine laid out as a real one is: an AMD CPU, a board chip with a temperature it gives no label for, SAS disks in the server's front and the
// shelf's front, an NVMe disk, and a disk in no enclosure that the kernel gives no temperature for.
func lab(t *testing.T) *sensorstest.Tree {
	t.Helper()

	tr := sensorstest.New(t)
	tr.Chip("k10temp", sensorstest.Temp{Label: "Tctl", Celsius: 54.5}, sensorstest.Temp{Label: "Tccd1", Celsius: 49})
	tr.Chip("nct6779", sensorstest.Temp{Label: "SYSTIN", Celsius: 31}, sensorstest.Temp{Celsius: 28})

	front := tr.Enclosure("1:0:24:0", server)
	annex := tr.Enclosure("8:0:24:0", shelf)
	tr.Disk(sensorstest.Disk{Name: "sdf", Serial: "ZR20B001", Model: "WUH722020BL5204", Celsius: new(41.0), Enclosure: front, Slot: 4})
	tr.Disk(sensorstest.Disk{Name: "sdbc", Serial: "ZR10A001", Model: "WUH722020BL5204", Celsius: new(47.0), Enclosure: annex, Slot: 1})
	tr.Disk(sensorstest.Disk{Name: "sdbd", Serial: "ZR10A002", Model: "WUH722020BL5204", Celsius: new(45.0), Enclosure: annex, Slot: 2})
	tr.Disk(sensorstest.Disk{Name: "sda", Model: "QEMU HARDDISK"})
	tr.NVMe("nvme0", "S6Z2NF0W123456", 38)

	return tr
}

func ids(rs []sensors.Reading) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}

	return out
}

// Every chip's temperature is read and named by chip and label, and the disks' are not among them.
func TestChips(t *testing.T) {
	t.Parallel()

	r, err := sensors.New(lab(t).Root, "").Chips(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"hwmon/k10temp/Tctl", "hwmon/k10temp/Tccd1", "hwmon/nct6779/SYSTIN", "hwmon/nct6779/temp2"}
	if got := ids(r.Readings); !slices.Equal(got, want) {
		t.Errorf("read %v, want %v", got, want)
	}
	if r.Readings[0].Celsius != 54.5 {
		t.Errorf("Tctl reads %g", r.Readings[0].Celsius)
	}
}

// Each disk is named by the enclosure it sits in and its serial number, and says where it is; an NVMe disk is in none, and gives its own
// temperature, not its parts'.
func TestDisks(t *testing.T) {
	t.Parallel()

	r, err := sensors.New(lab(t).Root, "").Disks(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"disk/5003048000c4d53f/ZR20B001", "disk/5003048000f8a93f/ZR10A001", "disk/5003048000f8a93f/ZR10A002", "disk/none/S6Z2NF0W123456"}
	if got := ids(r.Readings); !slices.Equal(got, want) {
		t.Fatalf("read %v, want %v", got, want)
	}

	d := r.Readings[1]
	if d.Celsius != 47 || d.Disk.Name != "sdbc" || d.Disk.Model != "WUH722020BL5204" || d.Disk.EnclosureName != "8:0:24:0" || d.Disk.Slot != "1" || d.Disk.Via != "drivetemp" {
		t.Errorf("the first shelf disk is %+v %+v", d, d.Disk)
	}
	if n := r.Readings[3]; n.Celsius != 38 || n.Disk.Name != "nvme0n1" || n.Disk.Via != "nvme" {
		t.Errorf("the NVMe disk is %+v %+v", n, n.Disk)
	}
}

// Only the readings wanted are read: a disk no zone follows is not asked its temperature, nor run through smartctl.
func TestOnlyWhatIsWanted(t *testing.T) {
	t.Parallel()

	tr := lab(t)
	shelfOnly := func(id string) bool { return strings.HasPrefix(id, "disk/5003048000f8a93f/") }

	r, err := sensors.New(tr.Root, "/nonexistent/smartctl").Disks(t.Context(), shelfOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Readings); !slices.Equal(got, []string{"disk/5003048000f8a93f/ZR10A001", "disk/5003048000f8a93f/ZR10A002"}) {
		t.Errorf("read %v", got)
	}
	if len(r.Unread) != 0 {
		t.Errorf("smartctl was run for a disk not wanted: %v", r.Unread)
	}

	r, err = sensors.New(tr.Root, "").Chips(t.Context(), func(id string) bool { return id == "hwmon/k10temp/Tctl" })
	if err != nil || !slices.Equal(ids(r.Readings), []string{"hwmon/k10temp/Tctl"}) {
		t.Errorf("read %v (%v)", ids(r.Readings), err)
	}
}

// Two chips of one name are told apart by a number, in the order of the devices they are on, as a board with two CPUs has.
func TestTwoChipsOfOneName(t *testing.T) {
	t.Parallel()

	tr := sensorstest.New(t)
	tr.Chip("k10temp", sensorstest.Temp{Label: "Tctl", Celsius: 50})
	tr.Chip("k10temp", sensorstest.Temp{Label: "Tctl", Celsius: 60})

	r, err := sensors.New(tr.Root, "").Chips(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Readings); !slices.Equal(got, []string{"hwmon/k10temp/Tctl", "hwmon/k10temp-2/Tctl"}) {
		t.Errorf("read %v", got)
	}
}

// A sensor that cannot be read is left out and said to be, and the rest are read.
func TestASensorThatFails(t *testing.T) {
	t.Parallel()

	tr := sensorstest.New(t)
	tr.Chip("k10temp", sensorstest.Temp{Label: "Tctl", Celsius: 50})
	annex := tr.Enclosure("8:0:24:0", shelf)
	broken := tr.Disk(sensorstest.Disk{Name: "sdbc", Serial: "ZR10A001", Celsius: new(47.0), Enclosure: annex, Slot: 1})
	tr.Disk(sensorstest.Disk{Name: "sdbd", Serial: "ZR10A002", Celsius: new(45.0), Enclosure: annex, Slot: 2})
	sensorstest.Break(t, filepath.Join(broken, "temp1_input"))

	r, err := sensors.New(tr.Root, "").Disks(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Readings); !slices.Equal(got, []string{"disk/5003048000f8a93f/ZR10A002"}) {
		t.Errorf("read %v", got)
	}
	if _, ok := r.Unread["disk/5003048000f8a93f/ZR10A001"]; !ok {
		t.Errorf("the broken disk is not said to be unread: %v", r.Unread)
	}
}

// A machine with no /sys, not Linux say, has nothing to read, which is not an error.
func TestNoSys(t *testing.T) {
	t.Parallel()

	m := sensors.New(t.TempDir(), "")
	if r, err := m.Chips(t.Context(), nil); err != nil || len(r.Readings) != 0 {
		t.Errorf("chips: %+v (%v)", r, err)
	}
	if r, err := m.Disks(t.Context(), nil); err != nil || len(r.Readings) != 0 {
		t.Errorf("disks: %+v (%v)", r, err)
	}
}

// smartctlScript is a smartctl that answers for sdz, says sdy is asleep, and fails for anything else.
const smartctlScript = `#!/bin/sh
for a; do disk=$a; done
case "$disk" in
/dev/sdz) echo '{"temperature":{"current":44}}' ;;
/dev/sdy) echo '{"smartctl":{"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}]}}'; exit 2 ;;
*) echo '{"smartctl":{"messages":[{"string":"Smartctl open device: '"$disk"' failed: No such device","severity":"error"}]}}'; exit 2 ;;
esac
`

// A disk the kernel gives no temperature for is asked with smartctl, a sleeping one is left asleep, and one smartctl cannot ask is said to be
// unread with what smartctl said.
func TestSmartctl(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to run the made-up smartctl with")
	}

	tr := sensorstest.New(t)
	tr.Disk(sensorstest.Disk{Name: "sdz", Serial: "ZA1", Model: "ST4000NM000A"})
	tr.Disk(sensorstest.Disk{Name: "sdy", Serial: "ZA2", Model: "ST4000NM000A"})
	tr.Disk(sensorstest.Disk{Name: "sdx", Serial: "ZA3", Model: "ST4000NM000A"})

	script := filepath.Join(t.TempDir(), "smartctl")
	if err := os.WriteFile(script, []byte(smartctlScript), 0o755); err != nil { //nolint:gosec // G306: a script the test runs
		t.Fatal(err)
	}

	r, err := sensors.New(tr.Root, script).Disks(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r.Readings); !slices.Equal(got, []string{"disk/none/ZA1"}) || r.Readings[0].Celsius != 44 || r.Readings[0].Disk.Via != "smartctl" {
		t.Errorf("read %+v", r.Readings)
	}
	if why := r.Unread["disk/none/ZA3"]; !strings.Contains(why, "No such device") {
		t.Errorf("sdx is unread for %q", why)
	}
	if _, ok := r.Unread["disk/none/ZA2"]; ok {
		t.Error("the sleeping disk is said to be unread")
	}
}

// Reading stops when the program is stopping.
func TestStopping(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sensors.New(lab(t).Root, "").Chips(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}
