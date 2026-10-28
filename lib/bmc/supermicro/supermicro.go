// Package supermicro sets the fans of Supermicro boards and disk shelves, with the commands Supermicro's own tools send. It is the maker the program
// assumes when the settings name none.
//
// Each board has fan zones, numbered from 0, that are set together as a percentage of full speed: on a mainboard zone 0 is the fans numbered
// FAN1, FAN2 and so on, and zone 1 the lettered ones, FANA and on. A mainboard in any fan mode but Full sets its fans itself and so takes back what
// it was set to within seconds; full_mode puts it in Full. A disk shelf's BMC keeps what it is set to in the mode it is in.
package supermicro

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
)

// The commands, all under Supermicro's own function number.
const (
	netFn = 0x30

	// modeCmd reads the fan mode with 0x00 and sets it with 0x01 and the mode.
	modeCmd  = 0x45
	modeGet  = 0x00
	modeSet  = 0x01
	modeFull = 0x01

	// dutyCmd with dutyOp reads a zone's duty with 0x00 and the zone, and sets it with 0x01, the zone and the duty, 0 to 100.
	dutyCmd = 0x70
	dutyOp  = 0x66
	dutyGet = 0x00
	dutySet = 0x01
)

// manufacturer is Supermicro's number among the companies IANA keeps.
const manufacturer = 10876

// Maker is Supermicro's way of setting fans, tried on a disk shelf's BMC (firmware 1.33) on 2026-10-20.
var Maker = bmc.Maker{Name: "supermicro", Tried: true, Manufacturers: []uint32{manufacturer}, New: New}

// modes are Supermicro's names for a mainboard's fan modes. A disk shelf's BMC has been seen to read 0 while its own page said Full, so on a shelf
// the names may not hold.
var modes = map[byte]string{0: "standard", 1: "full", 2: "optimal", 3: "pue", 4: "heavy io"}

// Fans is one Supermicro BMC's fans.
type Fans struct {
	conn     bmc.Conn
	fullMode bool

	mu sync.Mutex
	// taken says the mode has been read before it was first set, and found is that mode, to be put back
	taken bool
	found byte
}

func New(conn bmc.Conn, o bmc.Options) bmc.Fans {
	return &Fans{conn: conn, fullMode: o.FullMode}
}

func (f *Fans) readMode(ctx context.Context) (byte, error) {
	out, err := f.conn.Raw(ctx, netFn, modeCmd, modeGet)
	if err != nil {
		return 0, fmt.Errorf("reading the fan mode: %w", err)
	}
	if len(out) < 1 {
		return 0, errors.New("reading the fan mode: the BMC answered with nothing")
	}

	return out[0], nil
}

func (f *Fans) setMode(ctx context.Context, mode byte) error {
	if _, err := f.conn.Raw(ctx, netFn, modeCmd, modeSet, mode); err != nil {
		return fmt.Errorf("setting the fan mode to %s: %w", name(mode), err)
	}

	return nil
}

// name is a mode's name with its number, since a shelf's numbers may not mean what a mainboard's do: standard (0). One Supermicro does not name is
// its number alone.
func name(mode byte) string {
	if n, ok := modes[mode]; ok {
		return fmt.Sprintf("%s (%d)", n, mode)
	}

	return fmt.Sprintf("mode %d", mode)
}

func (f *Fans) Mode(ctx context.Context) (string, error) {
	m, err := f.readMode(ctx)
	if err != nil {
		return "", err
	}

	return name(m), nil
}

// Take puts the board in Full mode when the settings ask for it, and does nothing otherwise.
func (f *Fans) Take(ctx context.Context) (bool, error) {
	if !f.fullMode {
		return false, nil
	}

	m, err := f.readMode(ctx)
	if err != nil {
		return false, err
	}

	f.mu.Lock()
	if !f.taken {
		f.taken, f.found = true, m
	}
	f.mu.Unlock()

	if m == modeFull {
		return false, nil
	}

	return true, f.setMode(ctx, modeFull)
}

// Give puts the board back in the mode Take found it in, which then sets its fans itself again.
func (f *Fans) Give(ctx context.Context) error {
	f.mu.Lock()
	taken, found := f.taken, f.found
	f.mu.Unlock()

	if !taken || found == modeFull {
		return nil
	}

	return f.setMode(ctx, found)
}

func (f *Fans) Duty(ctx context.Context, zone int) (int, error) {
	if err := zoneOK(zone); err != nil {
		return 0, err
	}

	out, err := f.conn.Raw(ctx, netFn, dutyCmd, dutyOp, dutyGet, byte(zone)) //nolint:gosec // G115: zoneOK keeps it to a byte
	if err != nil {
		return 0, fmt.Errorf("reading zone %d's duty: %w", zone, err)
	}
	if len(out) < 1 {
		return 0, fmt.Errorf("reading zone %d's duty: the BMC answered with nothing", zone)
	}

	return int(out[0]), nil
}

func (f *Fans) SetDuty(ctx context.Context, zone, duty int) error {
	if err := zoneOK(zone); err != nil {
		return err
	}
	if duty < 0 || duty > 100 {
		return fmt.Errorf("%d%% is not a duty", duty)
	}

	if _, err := f.conn.Raw(ctx, netFn, dutyCmd, dutyOp, dutySet, byte(zone), byte(duty)); err != nil { //nolint:gosec // G115: both are checked to fit above
		return fmt.Errorf("setting zone %d to %d%%: %w", zone, duty, err)
	}

	return nil
}

func zoneOK(zone int) error {
	if zone < 0 || zone > 255 {
		return fmt.Errorf("%d is not a zone", zone)
	}

	return nil
}

// Zone is 0 for a numbered fan, FAN1, and 1 for a lettered one, FANA.
func (*Fans) Zone(fan string) int {
	rest := strings.TrimSpace(strings.ToUpper(fan))
	rest, ok := strings.CutPrefix(rest, "FAN")
	if !ok {
		return -1
	}

	rest = strings.TrimSpace(rest)
	if rest == "" {
		return -1
	}

	switch r := rune(rest[0]); {
	case unicode.IsDigit(r):
		return 0
	case unicode.IsLetter(r):
		return 1
	default:
		return -1
	}
}
