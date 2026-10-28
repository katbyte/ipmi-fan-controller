// Package sensorstest lays out a made-up machine's /sys for the tests, as the Linux kernel lays out a real one's: hwmon chips, SAS disks with the
// drivetemp driver's temperature and the enclosure slot each sits in, NVMe disks, and disks the kernel gives no temperature for.
package sensorstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Tree is a made-up machine: Root is its /.
type Tree struct {
	Root string
	t    testing.TB
	// hwmon is how many hwmon chips there are, which numbers the next
	hwmon int
	// target numbers the next SCSI device
	target int
}

// New is a machine with nothing in it.
func New(tb testing.TB) *Tree {
	tb.Helper()

	return &Tree{Root: tb.TempDir(), t: tb}
}

// Temp is one temperature a chip has: with no label, the kernel names it by its number.
type Temp struct {
	Label   string
	Celsius float64
}

func (tr *Tree) path(rel string) string {
	return filepath.Join(tr.Root, "sys", rel)
}

func (tr *Tree) dir(rel string) string {
	tr.t.Helper()

	p := tr.path(rel)
	if err := os.MkdirAll(p, 0o750); err != nil {
		tr.t.Fatal(err)
	}

	return p
}

func (tr *Tree) file(path string, content []byte) {
	tr.t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // G306: a made-up /sys, which anyone may read as a real one
		tr.t.Fatal(err)
	}
}

// link makes a link at from to to, written relative as the kernel writes its own.
func (tr *Tree) link(from, to string) {
	tr.t.Helper()

	rel, err := filepath.Rel(filepath.Dir(from), to)
	if err != nil {
		tr.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(from), 0o750); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.Symlink(rel, from); err != nil {
		tr.t.Fatal(err)
	}
}

// hwmonOn adds a hwmon chip of that name on a device, with its temperatures, and gives back its directory.
func (tr *Tree) hwmonOn(device, name string, temps []Temp) string {
	tr.t.Helper()

	tr.hwmon++
	n := "hwmon" + strconv.Itoa(tr.hwmon)
	dir := filepath.Join(device, "hwmon", n)
	tr.file(filepath.Join(dir, "name"), []byte(name+"\n"))
	tr.link(filepath.Join(dir, "device"), device)
	tr.link(tr.path("class/hwmon/"+n), dir)

	for i, t := range temps {
		file := filepath.Join(dir, fmt.Sprintf("temp%d_input", i+1))
		Set(tr.t, file, t.Celsius)
		if t.Label != "" {
			tr.file(filepath.Join(dir, fmt.Sprintf("temp%d_label", i+1)), []byte(t.Label+"\n"))
		}
	}

	return dir
}

// Chip adds a hwmon chip on a device of its own, k10temp say, and gives back its directory.
func (tr *Tree) Chip(name string, temps ...Temp) string {
	tr.t.Helper()

	return tr.hwmonOn(tr.dir(fmt.Sprintf("devices/platform/%s.%d", name, tr.hwmon)), name, temps)
}

// Enclosure is an enclosure the kernel knows, by its directory.
type Enclosure string

// Enclosure adds an enclosure: the kernel's name for it, 8:0:24:0, and its SAS address as the kernel gives it, 0x5003048000f8a93f.
func (tr *Tree) Enclosure(name, id string) Enclosure {
	tr.t.Helper()

	dir := tr.dir("devices/pci0000:00/0000:00:1c.0/0000:01:00.0/host8/target" + name + "/" + name + "/enclosure/" + name)
	tr.file(filepath.Join(dir, "id"), []byte(id+"\n"))

	return Enclosure(dir)
}

// Disk is a SCSI or SATA disk to lay out.
type Disk struct {
	Name, Serial, Model string
	// Celsius is its temperature through drivetemp, and nil for a disk the kernel gives none for.
	Celsius *float64
	// Enclosure and Slot are where it sits, and an empty Enclosure for a disk in none.
	Enclosure Enclosure
	Slot      int
}

// Disk adds a disk and gives back its drivetemp chip's directory, or "" for one without.
func (tr *Tree) Disk(d Disk) string {
	tr.t.Helper()

	tr.target++
	device := tr.dir(fmt.Sprintf("devices/pci0000:00/0000:00:1c.0/0000:01:00.0/host1/target1:0:%d/1:0:%d:0", tr.target, tr.target))
	tr.dir(fmt.Sprintf("devices/pci0000:00/0000:00:1c.0/0000:01:00.0/host1/target1:0:%d/1:0:%d:0/block/%s", tr.target, tr.target, d.Name))
	tr.link(tr.path("block/"+d.Name), filepath.Join(device, "block", d.Name))
	tr.link(filepath.Join(device, "block", d.Name, "device"), device)

	tr.file(filepath.Join(device, "model"), []byte(d.Model+"\n"))
	if d.Serial != "" {
		page := append([]byte{0x00, 0x80, 0x00, byte(len(d.Serial) + 4)}, []byte("    "+d.Serial)...) //nolint:gosec // G115: a serial number is a few dozen bytes
		tr.file(filepath.Join(device, "vpd_pg80"), page)
	}

	if d.Enclosure != "" {
		slot := filepath.Join(string(d.Enclosure), fmt.Sprintf("Slot%02d", d.Slot))
		tr.file(filepath.Join(slot, "slot"), []byte(strconv.Itoa(d.Slot)+"\n"))
		tr.link(filepath.Join(device, fmt.Sprintf("enclosure_device:Slot%02d", d.Slot)), slot)
	}

	if d.Celsius == nil {
		return ""
	}

	return tr.hwmonOn(device, "drivetemp", []Temp{{Celsius: *d.Celsius}})
}

// NVMe adds an NVMe disk, with its own temperature and one of its parts', and gives back its chip's directory.
func (tr *Tree) NVMe(name, serial string, celsius float64) string {
	tr.t.Helper()

	ctrl := tr.dir("devices/pci0000:00/0000:00:1c.1/0000:02:00.0/nvme/" + name)
	tr.file(filepath.Join(ctrl, "serial"), []byte(serial+"\n"))
	tr.file(filepath.Join(ctrl, "model"), []byte("Samsung SSD 990 PRO 2TB\n"))
	tr.dir("devices/pci0000:00/0000:00:1c.1/0000:02:00.0/nvme/" + name + "/" + name + "n1")

	return tr.hwmonOn(ctrl, "nvme", []Temp{{Label: "Composite", Celsius: celsius}, {Label: "Sensor 1", Celsius: celsius + 10}})
}

// Set sets the temperature in a chip's temperature file, temp1_input say, in thousandths of a degree as the kernel gives it.
func Set(tb testing.TB, file string, celsius float64) {
	tb.Helper()

	if err := os.WriteFile(file, []byte(strconv.Itoa(int(celsius*1000))+"\n"), 0o644); err != nil { //nolint:gosec // G306: a made-up /sys
		tb.Fatal(err)
	}
}

// Break makes a temperature file unreadable, as a sensor that fails to answer is: a directory in its place cannot be read as a file even by root.
func Break(tb testing.TB, file string) {
	tb.Helper()

	if err := os.Remove(file); err != nil {
		tb.Fatal(err)
	}
	if err := os.Mkdir(file, 0o750); err != nil {
		tb.Fatal(err)
	}
}
