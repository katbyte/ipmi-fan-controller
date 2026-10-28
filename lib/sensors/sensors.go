// Package sensors reads the temperatures a Linux machine keeps of itself, which need no root to read: every chip's under /sys/class/hwmon, which is
// the CPUs and the board's own sensors, and every disk's, with the shelf it sits in. A disk the kernel gives no temperature for is asked with
// smartctl, which does need root.
//
// Each reading is named for what it is, so the settings can pick readings by name: hwmon/k10temp/Tctl is an AMD CPU, hwmon/coretemp/Package id 0 an
// Intel one, and disk/5003048000f8a93f/ZR10A001 the disk of that serial number in the shelf whose enclosure has that SAS address. A disk in no shelf
// is under disk/none.
package sensors

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Reading is one temperature the machine reports.
type Reading struct {
	// ID is the reading's name: hwmon/k10temp/Tctl.
	ID      string  `json:"id"`
	Celsius float64 `json:"celsius"`
	// Disk is the disk the reading is of, for a disk.
	Disk *Disk `json:"disk,omitempty"`
}

// Disk is a disk as the kernel knows it.
type Disk struct {
	// Name is the kernel's name for it, which can change from one start to the next: sdf.
	Name   string `json:"name"`
	Serial string `json:"serial,omitempty"`
	Model  string `json:"model,omitempty"`
	// Enclosure is the SAS address of the enclosure it sits in, as sas-exporter's id label gives it, and EnclosureName the kernel's name for that
	// enclosure; both are empty for a disk in none.
	Enclosure     string `json:"enclosure,omitempty"`
	EnclosureName string `json:"enclosure_name,omitempty"`
	Slot          string `json:"slot,omitempty"`
	// Via is how its temperature was read: drivetemp or nvme, the kernel's drivers, or smartctl.
	Via string `json:"via"`
}

// Want says which readings to read, by name: nil for every one. A reading not wanted is not read at all, which for a disk saves it a command.
type Want func(id string) bool

func (w Want) wants(id string) bool {
	return w == nil || w(id)
}

// Read is one round of reading: what was read, and each sensor that is there but could not be read, with why.
type Read struct {
	Readings []Reading
	Unread   map[string]string
}

func (r *Read) unread(id string, err error) {
	if r.Unread == nil {
		r.Unread = map[string]string{}
	}

	r.Unread[id] = err.Error()
}

// NoEnclosure is where a disk in no enclosure is in its reading's name.
const NoEnclosure = "none"

// The hwmon drivers that are disks.
const (
	driveTemp = "drivetemp"
	nvmeTemp  = "nvme"
)

// Machine is the machine whose /sys is under root: / for the machine the program runs on.
type Machine struct {
	root string
	// smartctl is the smartctl to run for disks the kernel gives no temperature for, and "" for none.
	smartctl string
	// run runs a program and gives back what it printed: exec, or a test's own.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// New is the machine whose / is at root, asking disks the kernel gives no temperature for with that smartctl, or with none for "".
func New(root, smartctl string) *Machine {
	return &Machine{root: root, smartctl: smartctl, run: run}
}

// sys is a path under the machine's /sys.
func (m *Machine) sys(parts ...string) string {
	return filepath.Join(append([]string{m.root, "sys"}, parts...)...)
}

// chip is one hwmon directory.
type chip struct {
	dir, name, device string
}

// listChips is every hwmon directory, in the order of the devices they are on, so a second chip of the same name is always called the same.
func (m *Machine) listChips() ([]chip, error) {
	entries, err := os.ReadDir(m.sys("class", "hwmon"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []chip
	for _, e := range entries {
		dir, err := filepath.EvalSymlinks(m.sys("class", "hwmon", e.Name()))
		if err != nil {
			continue
		}

		c := chip{dir: dir, name: text(filepath.Join(dir, "name"))}
		if device, err := filepath.EvalSymlinks(filepath.Join(dir, "device")); err == nil {
			c.device = device
		}
		out = append(out, c)
	}

	slices.SortFunc(out, func(a, b chip) int {
		return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.device, b.device), cmp.Compare(a.dir, b.dir))
	})

	return out, nil
}

// Chips reads the hwmon temperatures wanted that are not a disk's. A chip whose name another has too is told apart by a number after its name,
// from 2, in the order of the devices they are on: k10temp and k10temp-2 on a board with two CPUs.
func (m *Machine) Chips(ctx context.Context, want Want) (Read, error) {
	chips, err := m.listChips()
	if err != nil {
		return Read{}, err
	}

	var out Read
	seen := map[string]int{}
	for _, c := range chips {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if c.name == driveTemp || c.name == nvmeTemp {
			continue
		}

		seen[c.name]++
		name := clean(cmp.Or(c.name, filepath.Base(c.dir)))
		if seen[c.name] > 1 {
			name += "-" + strconv.Itoa(seen[c.name])
		}

		for _, t := range temps(c.dir) {
			id := "hwmon/" + name + "/" + clean(t.label)
			if !want.wants(id) {
				continue
			}

			celsius, err := t.read()
			if err != nil {
				out.unread(id, err)
				continue
			}

			out.Readings = append(out.Readings, Reading{ID: id, Celsius: celsius})
		}
	}

	return out, nil
}

// temp is one temperature a chip has.
type temp struct {
	input, label string
}

func (t temp) read() (float64, error) {
	raw, err := os.ReadFile(t.input)
	if err != nil {
		return 0, err
	}

	milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, fmt.Errorf("%s holds %q, not a temperature", filepath.Base(t.input), strings.TrimSpace(string(raw)))
	}

	return milli / 1000, nil
}

// temps is every temperature a chip has, in the order of their numbers, each with the chip's label for it or temp1 for one it gives none.
func temps(dir string) []temp {
	inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))

	out := make([]temp, 0, len(inputs))
	for _, input := range inputs {
		n := strings.TrimSuffix(filepath.Base(input), "_input")
		label := text(filepath.Join(dir, n+"_label"))
		out = append(out, temp{input: input, label: cmp.Or(label, n)})
	}

	slices.SortFunc(out, func(a, b temp) int { return cmp.Compare(number(a.input), number(b.input)) })

	return out
}

// number is the number in a sensor file's name: 3 for temp3_input.
func number(file string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "temp"), "_input"))

	return n
}

// Disks reads the temperatures of the disks wanted: from the kernel's drivetemp and nvme drivers where it has them, and from smartctl for the disks
// it does not.
func (m *Machine) Disks(ctx context.Context, want Want) (Read, error) {
	chips, err := m.listChips()
	if err != nil {
		return Read{}, err
	}

	var out Read
	seen := map[string]bool{}
	for _, c := range chips {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if c.name != driveTemp && c.name != nvmeTemp {
			continue
		}

		d := disk(c)
		ts := temps(c.dir)
		if d.Name != "" {
			seen[d.Name] = true
		}
		if len(ts) == 0 {
			continue
		}

		// an NVMe disk's first temperature is the one it gives for itself as a whole; the rest are its parts'
		id := d.id()
		if !want.wants(id) {
			continue
		}

		celsius, err := ts[0].read()
		if err != nil {
			out.unread(id, err)
			continue
		}

		out.Readings = append(out.Readings, Reading{ID: id, Celsius: celsius, Disk: &d})
	}

	if m.smartctl != "" {
		m.smart(ctx, seen, want, &out)
	}

	slices.SortFunc(out.Readings, func(a, b Reading) int { return cmp.Compare(a.ID, b.ID) })

	return out, nil
}

// id is a disk's reading's name: under its enclosure's SAS address, by serial number, or by the kernel's name for it when it gives none.
func (d *Disk) id() string {
	enclosure := cmp.Or(d.Enclosure, d.EnclosureName, NoEnclosure)

	return "disk/" + clean(enclosure) + "/" + clean(cmp.Or(d.Serial, d.Name))
}

// disk is the disk a drivetemp or nvme chip is on.
func disk(c chip) Disk {
	d := Disk{Via: c.name}
	if c.device == "" {
		return d
	}

	switch c.name {
	case driveTemp:
		// the chip is on the disk's SCSI device, which has the disk below it
		describe(c.device, &d)

	case nvmeTemp:
		// the chip is on the NVMe controller, or on the PCI device the controller is below, by kernel
		ctrl := c.device
		if _, err := os.Stat(filepath.Join(ctrl, "serial")); err != nil {
			if found, _ := filepath.Glob(filepath.Join(c.device, "nvme", "nvme*")); len(found) > 0 {
				ctrl = found[0]
			}
		}

		d.Serial, d.Model = text(filepath.Join(ctrl, "serial")), text(filepath.Join(ctrl, "model"))
		if found, _ := filepath.Glob(filepath.Join(ctrl, filepath.Base(ctrl)+"n*")); len(found) > 0 {
			d.Name = filepath.Base(found[0])
		}
	}

	return d
}

// describe fills in what a SCSI or SATA disk's device says of it: its name, serial number, model, and the enclosure slot it sits in.
func describe(device string, d *Disk) {
	if blocks, err := os.ReadDir(filepath.Join(device, "block")); err == nil && len(blocks) > 0 {
		d.Name = blocks[0].Name()
	}

	d.Serial = serial(read(filepath.Join(device, "vpd_pg80")))
	d.Model = text(filepath.Join(device, "model"))
	d.Enclosure, d.EnclosureName, d.Slot = seat(device)
}

// seatPrefix starts the name of the link from a disk to the enclosure slot it sits in, which the kernel adds when the enclosure says so.
const seatPrefix = "enclosure_device:"

// seat is the enclosure a disk sits in, by its SAS address and the kernel's name for it, and the slot, as sas-exporter names them.
func seat(device string) (id, name, slot string) {
	entries, err := os.ReadDir(device)
	if err != nil {
		return "", "", ""
	}

	for _, e := range entries {
		named, ok := strings.CutPrefix(e.Name(), seatPrefix)
		if !ok {
			continue
		}

		at, err := filepath.EvalSymlinks(filepath.Join(device, e.Name()))
		if err != nil {
			return "", "", ""
		}

		enclosure := filepath.Dir(at)
		slot := text(filepath.Join(at, "slot"))
		if slot == "" || strings.HasPrefix(slot, "-") {
			slot = named
		}

		return address(text(filepath.Join(enclosure, "id"))), filepath.Base(enclosure), slot
	}

	return "", "", ""
}

// address is a SAS address as hex with nothing before it, as sas-exporter gives it: 5003048000f8a93f for the kernel's 0x5003048000f8a93f.
func address(s string) string {
	s = strings.ToLower(s)
	if rest, ok := strings.CutPrefix(s, "0x"); ok {
		return rest
	}

	return s
}

// The page of a disk's own description that holds its serial number, as the kernel hands it over: four bytes saying what it is and how long, and
// then the number.
const (
	serialPage       = 0x80
	serialPageHeader = 4
)

// serial is the serial number in that page, less the spaces it is padded with, and nothing when the page is not the one expected.
func serial(page []byte) string {
	if len(page) < serialPageHeader || page[1] != serialPage {
		return ""
	}

	body := page[serialPageHeader:]
	if length := int(page[2])<<8 | int(page[3]); length < len(body) {
		body = body[:length]
	}

	return strings.TrimSpace(strings.TrimRight(string(body), "\x00"))
}

// read is a file as it is, and nothing when it cannot be read.
func read(file string) []byte {
	b, err := os.ReadFile(file) //nolint:gosec // G304: the file is one of the kernel's under the machine's /sys
	if err != nil {
		return nil
	}

	return b
}

// text is what a file holds less the padding around it, and nothing when it cannot be read.
func text(file string) string {
	return strings.TrimSpace(string(read(file)))
}

// clean is a name as it can be one part of a reading's name: with no slash in it.
func clean(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "/", "_")
}
