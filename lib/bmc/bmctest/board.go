// Package bmctest is a made-up Supermicro BMC for the tests: it answers the fan commands as a real one does, reads the sensors a test gives it, and can
// be made to reset, set its own fans as a mainboard does, or stop answering.
package bmctest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
)

// ErrDown is what every command to a board that has stopped answering fails with.
var ErrDown = errors.New("the BMC is not answering")

// Board is a made-up Supermicro BMC.
type Board struct {
	mu      sync.Mutex
	device  bmc.Device
	shelf   bool
	mode    byte
	duty    map[int]int
	own     int
	sensors []bmc.Sensor
	down    bool
	writes  []string
	closed  int
	// sensorReads and modeReads count the times each was read
	sensorReads, modeReads int
}

// Mainboard is a board that sets its fans itself, at own percent, in any mode but Full, as a Supermicro mainboard does: what it is set to in another
// mode is taken back at once.
func Mainboard(own int) *Board {
	return &Board{device: bmc.Device{Manufacturer: 10876, Product: 0x1b5b, Firmware: "1.08"}, duty: map[int]int{0: own, 1: own}, own: own}
}

// Shelf is a disk shelf's BMC, found with zone 0 at duty, as a real shelf was. It keeps what it is set to until a test calls SetDuty, which is how a
// test plays the real shelf changing its own speed as its temperatures move.
func Shelf(duty int) *Board {
	return &Board{device: bmc.Device{Manufacturer: 10876, Product: 0xffff, Firmware: "1.33"}, shelf: true, duty: map[int]int{0: duty, 1: duty}}
}

// Sensor gives the board a sensor reading value, or replaces the one of that name.
func (b *Board) Sensor(name string, kind bmc.Kind, value float64) *Board {
	return b.put(bmc.Sensor{Name: name, Kind: kind, Value: value, Readable: true})
}

// Unreadable gives the board a sensor that has no reading, as a fan header with nothing plugged in has.
func (b *Board) Unreadable(name string, kind bmc.Kind) *Board {
	return b.put(bmc.Sensor{Name: name, Kind: kind})
}

func (b *Board) put(s bmc.Sensor) *Board {
	b.mu.Lock()
	defer b.mu.Unlock()

	if i := slices.IndexFunc(b.sensors, func(o bmc.Sensor) bool { return o.Name == s.Name }); i >= 0 {
		b.sensors[i] = s
	} else {
		b.sensors = append(b.sensors, s)
	}

	return b
}

// Down makes the board stop answering, or answer again.
func (b *Board) Down(down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.down = down
}

// Reset is the board restarting: back in Standard mode, and a mainboard setting its own fans again.
func (b *Board) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.mode = 0
	if !b.shelf {
		for z := range b.duty {
			b.duty[z] = b.own
		}
	}
}

// SetDuty is someone else setting a zone, the BMC's own web page say.
func (b *Board) SetDuty(zone, duty int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.duty[zone] = duty
}

// Duty is how fast a zone is set now.
func (b *Board) Duty(zone int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.dutyOf(zone)
}

func (b *Board) dutyOf(zone int) int {
	if !b.shelf && b.mode != 1 {
		return b.own
	}

	return b.duty[zone]
}

// Mode is the board's fan mode now.
func (b *Board) Mode() byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.mode
}

// Writes is every command that changed something, in the order sent: "mode 1", "zone 0 40".
func (b *Board) Writes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return slices.Clone(b.writes)
}

// Closed is how many times a connection to the board was closed.
func (b *Board) Closed() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.closed
}

// Raw answers Supermicro's fan commands, and refuses any other as a real BMC refuses a command it does not know.
func (b *Board) Raw(ctx context.Context, netFn, cmd byte, data ...byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.down {
		return nil, ErrDown
	}

	switch {
	case netFn == 0x30 && cmd == 0x45 && len(data) == 1 && data[0] == 0x00:
		b.modeReads++

		return []byte{b.mode}, nil

	case netFn == 0x30 && cmd == 0x45 && len(data) == 2 && data[0] == 0x01:
		b.mode = data[1]
		b.writes = append(b.writes, fmt.Sprintf("mode %d", data[1]))

		return nil, nil

	case netFn == 0x30 && cmd == 0x70 && len(data) == 3 && data[0] == 0x66 && data[1] == 0x00:
		return []byte{byte(b.dutyOf(int(data[2])))}, nil //nolint:gosec // G115: a duty is set from a byte, and is 0 to 100

	case netFn == 0x30 && cmd == 0x70 && len(data) == 4 && data[0] == 0x66 && data[1] == 0x01:
		if data[3] > 100 {
			return nil, &bmc.CompletionError{Code: 0xc9}
		}
		b.duty[int(data[2])] = int(data[3])
		b.writes = append(b.writes, fmt.Sprintf("zone %d %d", data[2], data[3]))

		return nil, nil
	}

	return nil, &bmc.CompletionError{Code: 0xc1}
}

func (b *Board) Sensors(ctx context.Context, want bmc.Want) ([]bmc.Sensor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.down {
		return nil, ErrDown
	}

	var out []bmc.Sensor
	for _, s := range b.sensors {
		if want.Wants(s.Name, s.Kind) {
			out = append(out, s)
		}
	}
	b.sensorReads++

	return out, nil
}

// SensorReads is how many times the board's sensors were read.
func (b *Board) SensorReads() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.sensorReads
}

// ModeReads is how many times the board's fan mode was read.
func (b *Board) ModeReads() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.modeReads
}

func (b *Board) Device(ctx context.Context) (bmc.Device, error) {
	if err := ctx.Err(); err != nil {
		return bmc.Device{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.down {
		return bmc.Device{}, ErrDown
	}

	return b.device, nil
}

func (b *Board) Close(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed++

	return nil
}

// Boards is a set of boards by the name the settings give each, which a test's controller dials in place of the network.
type Boards map[string]*Board

// Dial is the board of that name, or an error like a BMC that is not there gives.
func (bs Boards) Dial(_ context.Context, name string) (bmc.Conn, error) {
	b, ok := bs[name]
	if !ok {
		return nil, fmt.Errorf("no BMC called %s", name)
	}

	b.mu.Lock()
	down := b.down
	b.mu.Unlock()
	if down {
		return nil, ErrDown
	}

	return b, nil
}
