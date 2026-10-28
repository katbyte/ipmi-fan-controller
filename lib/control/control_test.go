package control_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/bmctest"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/makers"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/control"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
)

// shelfDisk and serverDisk name a disk in lab's shelf and one in its server, which the shelf does not follow.
const (
	shelfDisk  = "disk/5003048000f8a93f/"
	serverDisk = "disk/5003048000c4d53f/ZR20B001"
)

// machine is a made-up machine whose temperatures the test sets, and which counts the times its chips were read.
type machine struct {
	mu         sync.Mutex
	chips      []sensors.Reading
	chipsReads int
	disks      map[string]float64
	disksErr   error
}

func (m *machine) Chips(_ context.Context, want sensors.Want) (sensors.Read, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.chipsReads++

	var out sensors.Read
	for _, r := range m.chips {
		if want == nil || want(r.ID) {
			out.Readings = append(out.Readings, r)
		}
	}

	return out, nil
}

func (m *machine) Disks(_ context.Context, want sensors.Want) (sensors.Read, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.disksErr != nil {
		return sensors.Read{}, m.disksErr
	}

	// each disk is where its name says: disk/<enclosure>/<serial>
	var out sensors.Read
	for id, c := range m.disks {
		if want == nil || want(id) {
			parts := strings.Split(id, "/")
			out.Readings = append(out.Readings, sensors.Reading{ID: id, Celsius: c, Disk: &sensors.Disk{Name: parts[len(parts)-1], Enclosure: strings.TrimPrefix(parts[1], sensors.NoEnclosure)}})
		}
	}

	return out, nil
}

// disk sets a disk's temperature.
func (m *machine) disk(id string, celsius float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.disks == nil {
		m.disks = map[string]float64{}
	}
	m.disks[id] = celsius
}

func (m *machine) failDisks(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.disksErr = err
}

// clock is a clock the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// rig is a controller with its made-up machine, BMCs and clock.
type rig struct {
	*control.Controller

	machine *machine
	boards  bmctest.Boards
	clock   *clock
}

// settings reads settings as the program does, failing the test on a mistake.
func settings(t *testing.T, text string) *config.Config {
	t.Helper()

	c, err := config.Parse([]byte(text), makers.Names())
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// newRig is a controller that reads everything, as it does with the web page on.
func newRig(t *testing.T, text string, boards bmctest.Boards, dryRun bool) *rig {
	t.Helper()

	return rigFor(t, text, boards, dryRun, true)
}

func rigFor(t *testing.T, text string, boards bmctest.Boards, dryRun, readAll bool) *rig {
	t.Helper()

	r := &rig{machine: &machine{}, boards: boards, clock: &clock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}}
	r.Controller = control.New(control.Options{
		Config:  settings(t, text),
		Machine: r.machine,
		Dial:    func(ctx context.Context, b config.BMC) (bmc.Conn, error) { return boards.Dial(ctx, b.Name) },
		DryRun:  dryRun,
		ReadAll: readAll,
		Now:     r.clock.Now,
	})

	return r
}

// round moves the clock on by the interval and runs a round, giving back the zone of that name.
func (r *rig) round(t *testing.T, zone string) control.ZoneState {
	t.Helper()

	r.clock.add(10 * time.Second)
	r.Round(t.Context())

	for _, z := range r.State().Zones {
		if z.Name == zone {
			return z
		}
	}
	t.Fatalf("there is no zone %s", zone)

	return control.ZoneState{}
}

// lab is lab's shelf: its zone 0 follows the disks in its front, with the disks every round so a test need not wait a minute for them.
const lab = `
interval: 10s
disk_interval: 10s
bmcs:
  - name: annex
    host: annex-ipmi.example.net
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

// The shelf runs as fast as its hottest disk asks, ignoring the server's; it slows only once the disks have cooled by the hysteresis; and when the
// program stops it is put back at the 60% it was found at.
func TestTheShelfFollowsItsDisks(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.machine.disk(shelfDisk+"ZR10A002", 40)
	r.machine.disk(serverDisk, 58)

	z := r.round(t, "shelf")
	if z.State != control.Auto || z.Want != 75 || shelf.Duty(0) != 75 || pointer.From(z.Found) != 60 {
		t.Fatalf("the shelf is %+v at %d%%, want 75%% from its hottest disk at 50°", z, shelf.Duty(0))
	}
	if !strings.Contains(z.Why, "ZR10A001, the hottest of 2") {
		t.Errorf("why: %s", z.Why)
	}

	// 6° cooler: the curve asks 48%, and 2° hotter than that asks 55%, so it comes down to 55
	r.machine.disk(shelfDisk+"ZR10A001", 44)
	if z := r.round(t, "shelf"); z.Want != 55 {
		t.Errorf("at 44° it wants %d%%, want 55%%: %s", z.Want, z.Why)
	}

	// no cooler, so no slower
	if z := r.round(t, "shelf"); z.Want != 55 {
		t.Errorf("still at 44° it wants %d%%, want 55%%", z.Want)
	}

	// hotter again goes up at once
	r.machine.disk(shelfDisk+"ZR10A001", 46)
	if z := r.round(t, "shelf"); z.Want != 55 {
		t.Errorf("at 46° it wants %d%%, want 55%%", z.Want)
	}
	r.machine.disk(shelfDisk+"ZR10A001", 48)
	if z := r.round(t, "shelf"); z.Want != 65 || shelf.Duty(0) != 65 {
		t.Errorf("at 48° it wants %d%%, want 65%%", z.Want)
	}

	r.Stop(t.Context())
	if got := shelf.Writes(); !slices.Equal(got, []string{"zone 0 75", "zone 0 55", "zone 0 65", "zone 0 60"}) {
		t.Errorf("sent %v", got)
	}
	if shelf.Duty(0) != 60 {
		t.Errorf("the shelf was left at %d%%, want the 60%% it was found at", shelf.Duty(0))
	}
}

// A shelf changes its own speed as its temperatures move, as a real one did overnight: the program sets it back the next round and counts it, without
// touching the shelf's mode, and still puts it back at the speed it was first found at when it stops.
func TestAShelfChangingItsOwnSpeed(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(50)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)

	if z := r.round(t, "shelf"); shelf.Duty(0) != 75 || z.Takeovers != 0 {
		t.Fatalf("the shelf is at %d%% with %d takeovers, want 75%% and none", shelf.Duty(0), z.Takeovers)
	}

	shelf.SetDuty(0, 60)
	z := r.round(t, "shelf")
	if shelf.Duty(0) != 75 || z.Takeovers != 1 {
		t.Errorf("after the shelf went to 60%% itself it is at %d%% with %d takeovers, want 75%% and 1", shelf.Duty(0), z.Takeovers)
	}
	if len(z.Corrections) != 1 {
		t.Fatalf("noted %d corrections, want 1: %+v", len(z.Corrections), z.Corrections)
	}
	if fix := z.Corrections[0]; fix.Why != control.Changed || pointer.From(fix.Found) != 60 || fix.Was != 75 || fix.Set != 75 || fix.Says != "found at 60%, not the 75% it was set to: its BMC or someone else changed it, so it was set to 75%" {
		t.Errorf("noted %+v", fix)
	}

	// changed to just what it wants by then: counted and noted, and nothing sent
	shelf.SetDuty(0, 80)
	r.machine.disk(shelfDisk+"ZR10A001", 51)
	z = r.round(t, "shelf")
	if len(z.Corrections) != 2 || z.Takeovers != 2 || !strings.HasSuffix(z.Corrections[1].Says, "to the 80% it wants now, so it was left there") {
		t.Errorf("noted %+v", z.Corrections)
	}

	r.Stop(t.Context())
	if got := shelf.Writes(); !slices.Equal(got, []string{"zone 0 75", "zone 0 75", "zone 0 50"}) {
		t.Errorf("sent %v, want the zone set, set again, and put back, and the mode never set", got)
	}
}

// When the disks stop answering, the shelf keeps to what they last said for three of their intervals, then runs at its failsafe speed until they
// answer again. A follow that matches nothing at all fails safe at once.
func TestFailingSafe(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 40)
	if z := r.round(t, "shelf"); z.State != control.Auto || z.Want != 40 {
		t.Fatalf("the shelf is %s at %d%%", z.State, z.Want)
	}

	r.machine.failDisks(errors.New("the kernel would not say"))
	for range 3 {
		if z := r.round(t, "shelf"); z.State != control.Auto {
			t.Fatalf("the shelf failed safe after the disks missed fewer than three rounds: %s", z.Why)
		}
	}

	z := r.round(t, "shelf")
	if z.State != control.Failsafe || z.Want != 100 || shelf.Duty(0) != 100 || !strings.Contains(z.Why, "disks stopped answering") {
		t.Fatalf("with the disks gone the shelf is %s at %d%%: %s", z.State, shelf.Duty(0), z.Why)
	}

	// back from full speed it comes down only as far as 2° hotter allows
	r.machine.failDisks(nil)
	if z := r.round(t, "shelf"); z.State != control.Auto || z.Want != 44 {
		t.Errorf("with the disks back the shelf is %s at %d%%, want 44%%", z.State, z.Want)
	}

	// every disk pulled: nothing to follow
	r.machine.mu.Lock()
	r.machine.disks = nil
	r.machine.mu.Unlock()
	if z := r.round(t, "shelf"); z.State != control.Failsafe || !strings.Contains(z.Why, "no sensor matches") {
		t.Errorf("with no disks the shelf is %s: %s", z.State, z.Why)
	}
}

// mainboard is tower's own board: zone 0 follows the CPU, and it is put in Full mode to keep what it is set to.
const mainboard = `
bmcs:
  - name: tower
    full_mode: true
zones:
  - name: cpu
    bmc: tower
    zone: 0
    follow:
      - sensors: [hwmon/k10temp/Tctl, ipmi/tower/CPU Temp]
        curve: {40: 30, 80: 100}
`

// A mainboard is put in Full mode before its zone is set, readied again when it resets, and a zone found set to something else is counted and set
// back. When the program stops the board is put back in the mode it was in.
func TestAMainboardTakingItsFansBack(t *testing.T) {
	t.Parallel()

	board := bmctest.Mainboard(50).Sensor("CPU Temp", bmc.Temperature, 60).Sensor("FAN1", bmc.Fan, 1800)
	r := newRig(t, mainboard, bmctest.Boards{"tower": board}, false)

	z := r.round(t, "cpu")
	if board.Mode() != 1 || board.Duty(0) != 65 || z.Want != 65 || z.Takeovers != 0 {
		t.Fatalf("after one round the board is in mode %d at %d%%: %+v", board.Mode(), board.Duty(0), z)
	}

	board.Reset()
	z = r.round(t, "cpu")
	if board.Mode() != 1 || board.Duty(0) != 65 {
		t.Errorf("after a reset the board is in mode %d at %d%%", board.Mode(), board.Duty(0))
	}
	if b := r.State().BMCs[0]; b.Retakes != 1 {
		t.Errorf("the board was readied again %d times, want 1", b.Retakes)
	}
	if z.Resets != 1 || len(z.Corrections) != 1 || z.Corrections[0].Why != control.Reset || z.Corrections[0].Set != 65 {
		t.Errorf("after the reset the zone noted %d resets: %+v", z.Resets, z.Corrections)
	}

	board.SetDuty(0, 100)
	z = r.round(t, "cpu")
	if board.Duty(0) != 65 || z.Takeovers != 1 {
		t.Errorf("after someone else set it the zone is at %d%% with %d takeovers", board.Duty(0), z.Takeovers)
	}

	r.Stop(t.Context())
	if board.Mode() != 0 {
		t.Errorf("the board was left in mode %d, want the standard mode it was found in", board.Mode())
	}
}

// A dry run reads everything and sets nothing, and says what it would set.
func TestADryRun(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, true)
	r.machine.disk(shelfDisk+"ZR10A001", 50)

	z := r.round(t, "shelf")
	r.Stop(t.Context())

	if len(shelf.Writes()) != 0 {
		t.Errorf("a dry run sent %v", shelf.Writes())
	}
	if z.Want != 75 || pointer.From(z.Duty) != 60 || !strings.HasPrefix(z.Why, "would be set to 75%") || !r.State().DryRun {
		t.Errorf("the zone is %+v", z)
	}
}

// A quiet hold runs the zone at its speed, never under the floor, until the disks reach the top of the curve; a full hold runs it flat out; and
// a hold ends when its time is up or it is let go of.
func TestHolds(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 44)

	if err := r.Hold("shelf", control.Hold{Duty: 20, Until: r.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if z := r.round(t, "shelf"); z.State != control.Quiet || z.Want != 30 {
		t.Errorf("held quiet at 20%% the shelf is %s at %d%%, want the floor of 30%%", z.State, z.Want)
	}

	r.machine.disk(shelfDisk+"ZR10A001", 56)
	if z := r.round(t, "shelf"); z.State != control.Auto || z.Want != 100 || !strings.Contains(z.Why, "too hot to stay quiet") {
		t.Errorf("held quiet with a disk at 56° the shelf is %s at %d%%: %s", z.State, z.Want, z.Why)
	}

	r.machine.disk(shelfDisk+"ZR10A001", 44)
	if err := r.Hold("shelf", control.Hold{Full: true}); err != nil {
		t.Fatal(err)
	}
	if z := r.round(t, "shelf"); z.State != control.Full || shelf.Duty(0) != 100 {
		t.Errorf("held at full speed the shelf is %s at %d%%", z.State, shelf.Duty(0))
	}

	if err := r.Release("shelf"); err != nil {
		t.Fatal(err)
	}
	if z := r.round(t, "shelf"); z.State != control.Auto {
		t.Errorf("let go, the shelf is %s", z.State)
	}

	if err := r.Hold("shelf", control.Hold{Duty: 40, Until: r.clock.Now().Add(15 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	r.round(t, "shelf")
	if z := r.round(t, "shelf"); z.State != control.Auto || z.Hold != nil {
		t.Errorf("after its time the hold is still on: %+v", z)
	}

	if err := r.Hold("attic", control.Hold{Full: true}); err == nil {
		t.Error("a zone that is not there was held")
	}
	if err := r.Release("shelf"); err == nil {
		t.Error("a zone not held was let go of")
	}
}

// A BMC that stops answering leaves its zone where it was and is said to be unreachable; when it answers again it is dialed again and set, and is
// still put back at the speed it was first found at.
func TestABMCThatStopsAnswering(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.round(t, "shelf")

	shelf.Down(true)
	z := r.round(t, "shelf")
	if z.State != control.Unreachable || r.State().BMCs[0].Up {
		t.Errorf("with the BMC down the zone is %s", z.State)
	}

	shelf.Down(false)
	r.machine.disk(shelfDisk+"ZR10A001", 55)
	if z := r.round(t, "shelf"); z.State != control.Auto || shelf.Duty(0) != 100 {
		t.Errorf("with the BMC back the zone is %s at %d%%", z.State, shelf.Duty(0))
	}
	if shelf.Closed() != 1 {
		t.Errorf("the connection was closed %d times, want once when the BMC stopped answering", shelf.Closed())
	}

	r.Stop(t.Context())
	if shelf.Duty(0) != 60 {
		t.Errorf("the shelf was left at %d%%, want 60%%", shelf.Duty(0))
	}
}

// A BMC whose connection was dropped when the program stops, by a round the stop cut short say, is dialed again to have its fans handed back.
func TestHandingBackAfterTheConnectionDropped(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.round(t, "shelf")

	shelf.Down(true)
	r.round(t, "shelf")
	shelf.Down(false)

	r.Stop(t.Context())
	if shelf.Duty(0) != 60 {
		t.Errorf("the shelf was left at %d%%, want the 60%% it was found at", shelf.Duty(0))
	}
}

// on_stop: full leaves the zone at full speed when the program stops, and the board in the mode the program put it in.
func TestStoppingAtFullSpeed(t *testing.T) {
	t.Parallel()

	board := bmctest.Mainboard(50).Sensor("CPU Temp", bmc.Temperature, 50)
	r := newRig(t, strings.Replace(mainboard, "full_mode: true", "full_mode: true\n    on_stop: full", 1), bmctest.Boards{"tower": board}, false)
	r.round(t, "cpu")
	r.Stop(t.Context())

	if board.Duty(0) != 100 || board.Mode() != 1 {
		t.Errorf("the board was left in mode %d at %d%%", board.Mode(), board.Duty(0))
	}
}

// New settings are taken up at once: a changed curve is followed, and a zone no longer set is put back as it was found.
func TestNewSettings(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.round(t, "shelf")

	r.Apply(settings(t, strings.Replace(lab, "{35: 30, 45: 50, 55: 100}", "{35: 30, 50: 40, 60: 100}", 1)))
	if z := r.round(t, "shelf"); z.Want != 40 || shelf.Duty(0) != 40 {
		t.Errorf("with the new curve the shelf wants %d%%, want 40%%", z.Want)
	}

	r.Apply(settings(t, strings.Replace(lab, "  - name: shelf\n    bmc: annex\n    zone: 0", "  - name: shelf\n    bmc: annex\n    zone: 1", 1)))
	r.round(t, "shelf")
	if shelf.Duty(0) != 60 {
		t.Errorf("zone 0, no longer set, was left at %d%%, want the 60%% it was found at", shelf.Duty(0))
	}
}

// What the program reads is all in its state: every temperature with the zones that follow it, the BMC's own temperatures as ipmi/ readings, and a
// fan that has stopped, apart from one with no reading at all.
// With the web page on, each round is kept for its charts: each zone's speed and temperatures, each fan and each reading, for six hours. A round
// with no value for a series has none, and a series with nothing left in the six hours is dropped.
func TestHistory(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60).Sensor("FAN1", bmc.Fan, 4500)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.round(t, "shelf")

	r.machine.disks = nil
	shelf.Sensor("FAN1", bmc.Fan, 5100)
	r.round(t, "shelf")

	h := r.History(0)
	if !slices.Equal(h.Rounds, []int64{1, 2}) || len(h.At) != 2 {
		t.Fatalf("kept rounds %v at %v, want 1 and 2", h.Rounds, h.At)
	}
	if got := h.Series["duty/shelf"]; len(got) != 2 || pointer.From(got[0]) != 75 {
		t.Errorf("the shelf's speed is %v", got)
	}
	if got := h.Series["fan/annex/FAN1"]; len(got) != 2 || pointer.From(got[0]) != 4500 || pointer.From(got[1]) != 5100 {
		t.Errorf("FAN1 is %v", got)
	}
	if got := h.Series["temp/"+shelfDisk+"ZR10A001"]; len(got) != 2 || pointer.From(got[0]) != 50 {
		t.Errorf("the disk is %v", got)
	}
	if got := h.Series["follow/shelf/disks"]; len(got) != 2 || pointer.From(got[0]) != 50 {
		t.Errorf("what the shelf follows is %v", got)
	}

	if after := r.History(1); !slices.Equal(after.Rounds, []int64{2}) || len(after.Series["fan/annex/FAN1"]) != 1 {
		t.Errorf("after round 1 kept %v: %v", after.Rounds, after.Series["fan/annex/FAN1"])
	}

	// seven hours on, the first two rounds are gone, and the disk, gone since, with them
	r.clock.add(7 * time.Hour)
	r.round(t, "shelf")
	h = r.History(0)
	if !slices.Equal(h.Rounds, []int64{3}) {
		t.Errorf("seven hours on kept rounds %v, want only 3", h.Rounds)
	}
	if _, ok := h.Series["temp/"+shelfDisk+"ZR10A001"]; ok {
		t.Error("a disk gone for six hours is still kept")
	}
}

// A test of which disks a zone cools runs it flat out, and compares every disk with how hot it was when the test began: the shelf's own disks cool,
// the server's do not. When its time is up the zone goes back to its curve. One test runs at a time, none in a dry run, and one can be stopped.
func TestWhichDisksAZoneCools(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60)
	r := newRig(t, lab, bmctest.Boards{"annex": shelf}, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.machine.disk(shelfDisk+"ZR10A002", 48)
	r.machine.disk(serverDisk, 45)
	r.round(t, "shelf")

	if err := r.StartProbe("shelf", 2*time.Minute); err == nil {
		t.Error("a two minute test was started")
	}
	if err := r.StartProbe("attic", 15*time.Minute); err == nil {
		t.Error("a zone that is not there was tested")
	}
	if err := r.StartProbe("shelf", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := r.StartProbe("shelf", 15*time.Minute); err == nil {
		t.Error("a second test was started while the first ran")
	}

	z := r.round(t, "shelf")
	if shelf.Duty(0) != 100 || z.State != control.Full {
		t.Errorf("under test the shelf is %s at %d%%, want flat out", z.State, shelf.Duty(0))
	}

	r.machine.disk(shelfDisk+"ZR10A001", 44)
	r.machine.disk(shelfDisk+"ZR10A002", 43)
	r.clock.add(15 * time.Minute)
	z = r.round(t, "shelf")

	p := r.State().Probe
	if p == nil || !p.Done || p.Stopped || len(p.Disks) != 3 {
		t.Fatalf("the test is %+v", p)
	}
	for _, d := range p.Disks {
		cooled := d.Before - d.Now
		if (d.Enclosure == "5003048000f8a93f") != (cooled > 4) {
			t.Errorf("%s in %q cooled by %.1f°", d.Name, d.Enclosure, cooled)
		}
	}
	if z.State != control.Auto || shelf.Duty(0) == 100 {
		t.Errorf("after the test the shelf is %s at %d%%, want back on its curve", z.State, shelf.Duty(0))
	}

	// put away, then another, stopped early
	if err := r.StopProbe("shelf"); err != nil || r.State().Probe == nil {
		t.Errorf("putting the test away: %v", err)
	}
	r.round(t, "shelf")
	if p := r.State().Probe; p != nil {
		t.Errorf("a test put away is still there: %+v", p)
	}
	if err := r.StartProbe("shelf", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	r.round(t, "shelf")
	if err := r.StopProbe("shelf"); err != nil {
		t.Fatal(err)
	}
	z = r.round(t, "shelf")
	if p := r.State().Probe; p == nil || !p.Done || !p.Stopped || z.State != control.Auto {
		t.Errorf("a stopped test is %+v, and the shelf %s", p, z.State)
	}

	dry := newRig(t, lab, bmctest.Boards{"annex": bmctest.Shelf(60)}, true)
	if err := dry.StartProbe("shelf", 15*time.Minute); err == nil {
		t.Error("a dry run started a test")
	}
}

// Without the web page nothing is kept for charts.
func TestNoHistoryWithoutTheWebPage(t *testing.T) {
	t.Parallel()

	r := rigFor(t, lab, bmctest.Boards{"annex": bmctest.Shelf(60)}, false, false)
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.round(t, "shelf")

	if h := r.History(0); len(h.Rounds) != 0 || len(h.Series) != 0 {
		t.Errorf("kept %+v", h)
	}
}

func TestWhatItSees(t *testing.T) {
	t.Parallel()

	board := bmctest.Mainboard(50).Sensor("CPU Temp", bmc.Temperature, 60).Sensor("FAN1", bmc.Fan, 1800).Sensor("FANA", bmc.Fan, 0).Unreadable("FAN8", bmc.Fan)
	r := newRig(t, mainboard, bmctest.Boards{"tower": board}, false)
	r.machine.chips = []sensors.Reading{{ID: "hwmon/k10temp/Tctl", Celsius: 70}}

	z := r.round(t, "cpu")
	st := r.State()

	if z.Want != 83 || z.Follows[0].Hottest != "hwmon/k10temp/Tctl" {
		t.Errorf("the zone is %+v", z)
	}
	if !slices.Equal(z.Fans, []string{"FAN1", "FAN8"}) {
		t.Errorf("the zone's fans are %v", z.Fans)
	}

	var cpu control.ReadingState
	for _, rd := range st.Readings {
		if rd.ID == "ipmi/tower/CPU Temp" {
			cpu = rd
		}
	}
	if cpu.Celsius != 60 || cpu.Source != "ipmi/tower" || !slices.Equal(cpu.Zones, []string{"cpu"}) {
		t.Errorf("the BMC's CPU temperature is %+v", cpu)
	}

	fans := st.BMCs[0].Fans
	if len(fans) != 3 || fans[1].Name != "FANA" || !fans[1].Stopped || fans[1].Zone != 1 || fans[2].Stopped {
		t.Errorf("the fans are %+v", fans)
	}
}

// With the web page off, a round reads only what the zones follow and what setting them needs: no chip, no disk no zone follows, no BMC sensor,
// no fan and no fan mode.
func TestReadingOnlyWhatIsFollowed(t *testing.T) {
	t.Parallel()

	shelf := bmctest.Shelf(60).Sensor("SAS3 F1 Temp", bmc.Temperature, 68).Sensor("FAN1", bmc.Fan, 4500)
	r := rigFor(t, lab, bmctest.Boards{"annex": shelf}, false, false)
	r.machine.chips = []sensors.Reading{{ID: "hwmon/k10temp/Tctl", Celsius: 70}}
	r.machine.disk(shelfDisk+"ZR10A001", 50)
	r.machine.disk(serverDisk, 58)

	z := r.round(t, "shelf")
	if z.Want != 75 || shelf.Duty(0) != 75 {
		t.Fatalf("the shelf wants %d%% and is at %d%%, want 75%%", z.Want, shelf.Duty(0))
	}

	st := r.State()
	if r.machine.chipsReads != 0 || shelf.SensorReads() != 0 || shelf.ModeReads() != 0 {
		t.Errorf("read the chips %d times, the shelf's sensors %d times and its mode %d times, want none", r.machine.chipsReads, shelf.SensorReads(), shelf.ModeReads())
	}
	if len(st.Readings) != 1 || st.Readings[0].ID != shelfDisk+"ZR10A001" || len(st.BMCs[0].Fans) != 0 || st.BMCs[0].Mode != "" {
		t.Errorf("the state has %+v and fans %+v", st.Readings, st.BMCs[0].Fans)
	}

	// a zone that follows the BMC's own sensor has that sensor read, and no other
	r = rigFor(t, strings.Replace(lab, "sensors: [disk/5003048000f8a93f/*]", `sensors: ["ipmi/annex/SAS3 F1 Temp"]`, 1), bmctest.Boards{"annex": shelf}, false, false)
	r.round(t, "shelf")
	if got := r.State().Readings; len(got) != 1 || got[0].ID != "ipmi/annex/SAS3 F1 Temp" || len(r.State().BMCs[0].Fans) != 0 {
		t.Errorf("following the shelf's chip read %+v", got)
	}
}
