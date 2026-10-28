package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/makers"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
)

// Dial connects to the BMC the settings describe.
type Dial func(ctx context.Context, b config.BMC) (bmc.Conn, error)

// errNotConnected is what a BMC whose connection has been dropped answers until it is dialed again.
var errNotConnected = errors.New("not connected")

// relay is a BMC's connection as its maker's fans see it: the same one across reconnects, so what the maker found on the BMC the first time, the
// mode it was in, is still there to be put back when the program stops.
type relay struct {
	mu   sync.Mutex
	conn bmc.Conn
}

func (r *relay) get() (bmc.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn == nil {
		return nil, errNotConnected
	}

	return r.conn, nil
}

func (r *relay) Raw(ctx context.Context, netFn, cmd byte, data ...byte) ([]byte, error) {
	c, err := r.get()
	if err != nil {
		return nil, err
	}

	return c.Raw(ctx, netFn, cmd, data...)
}

func (r *relay) Sensors(ctx context.Context, want bmc.Want) ([]bmc.Sensor, error) {
	c, err := r.get()
	if err != nil {
		return nil, err
	}

	return c.Sensors(ctx, want)
}

func (r *relay) Device(ctx context.Context) (bmc.Device, error) {
	c, err := r.get()
	if err != nil {
		return bmc.Device{}, err
	}

	return c.Device(ctx)
}

// Close closes the connection and forgets it, so the next round dials again.
func (r *relay) Close(ctx context.Context) error {
	r.mu.Lock()
	c := r.conn
	r.conn = nil
	r.mu.Unlock()

	if c == nil {
		return nil
	}

	return c.Close(ctx)
}

// link is one BMC the program talks to, kept across rounds.
type link struct {
	cfg   config.BMC
	maker bmc.Maker
	relay *relay
	fans  bmc.Fans

	up      bool
	err     string
	device  bmc.Device
	mode    string
	sensors []bmc.Sensor
	// duties is how fast each of its zones is set, by zone number, as read this round, and dutyErr why one could not be read
	duties  map[int]int
	dutyErr map[int]error

	// taken says the program has readied the BMC, and so has something to give back
	taken   bool
	retakes int64
	// stopped is the fans last seen stopped, so a fan stopping is said once
	stopped map[string]bool
}

// newLink is a BMC not yet dialed.
func newLink(b config.BMC) (*link, error) {
	maker, ok := makers.Get(b.Maker)
	if !ok {
		return nil, fmt.Errorf("%s: no maker called %s", b.Name, b.Maker)
	}
	if !maker.Tried {
		clog.Log.Warnf("%s: the %s way of setting fans has not been tried on a real BMC yet: watch its fans the first time", b.Name, maker.Name)
	}

	r := &relay{}

	return &link{cfg: b, maker: maker, relay: r, fans: maker.New(r, bmc.Options{FullMode: b.FullMode}), stopped: map[string]bool{}}, nil
}

// refused says an error is the BMC answering that it would not, rather than not answering.
func refused(err error) bool {
	var c *bmc.CompletionError

	return errors.As(err, &c)
}

// asking is what a round asks a BMC beyond its zones' duties.
type asking struct {
	// sensors says its sensors are read at all, and want which of them
	sensors bool
	want    bmc.Want
	// mode says its fan mode is read, for the page to show
	mode bool
}

// read reads the BMC: dialing it when it is not connected, then the sensors and the fan mode asked for, and the duty of each of the zones given. A
// BMC that does not answer is dropped, to be dialed again next round. It gives back the BMC's temperatures as readings.
func (l *link) read(ctx context.Context, dial Dial, zones []int, ask asking) (readings []sensors.Reading, unread map[string]string) {
	l.duties, l.dutyErr = map[int]int{}, map[int]error{}

	if _, err := l.relay.get(); err != nil {
		conn, err := dial(ctx, l.cfg)
		if err != nil {
			l.down(ctx, err)
			return nil, nil
		}

		l.relay.mu.Lock()
		l.relay.conn = conn
		l.relay.mu.Unlock()

		device, err := conn.Device(ctx)
		if err != nil && !refused(err) {
			l.down(ctx, err)
			return nil, nil
		}
		l.device = device
		if by := makers.Of(device.Manufacturer); err == nil && by != "" && by != l.maker.Name {
			clog.Log.Warnf("%s: the BMC says %s made it, but the settings say %s", l.cfg.Name, by, l.maker.Name)
		}
		clog.Log.Infof("%s: connected (maker number %d, product %#04x, firmware %s)", l.cfg.Name, device.Manufacturer, device.Product, device.Firmware)
	}

	var all []bmc.Sensor
	if ask.sensors {
		var err error
		if all, err = l.relay.Sensors(ctx, ask.want); err != nil {
			l.down(ctx, err)
			return nil, nil
		}
	}
	l.sensors = all

	var problems []string
	l.mode = ""
	if ask.mode {
		if mode, err := l.fans.Mode(ctx); err != nil {
			if !refused(err) {
				l.down(ctx, err)
				return nil, nil
			}
			problems = append(problems, err.Error())
		} else {
			l.mode = mode
		}
	}

	for _, z := range zones {
		d, err := l.fans.Duty(ctx, z)
		switch {
		case err == nil:
			l.duties[z] = d
		case errors.Is(err, bmc.ErrUnreadable) || refused(err):
			l.dutyErr[z] = err
		default:
			l.down(ctx, err)
			return nil, nil
		}
	}

	if !l.up {
		clog.Log.Infof("%s: answering", l.cfg.Name)
	}
	l.up, l.err = true, strings.Join(problems, "; ")

	unread = map[string]string{}
	for _, s := range all {
		if s.Kind != bmc.Temperature {
			continue
		}

		id := "ipmi/" + l.cfg.Name + "/" + strings.ReplaceAll(s.Name, "/", "_")
		if !s.Readable {
			unread[id] = "the BMC has no reading for it"
			continue
		}

		readings = append(readings, sensors.Reading{ID: id, Celsius: s.Value})
	}

	l.fanAlarms()

	return readings, unread
}

// down marks the BMC as not answering and drops its connection. A round cut short by the program stopping is not the BMC's doing, and is not logged.
func (l *link) down(ctx context.Context, err error) {
	if (l.up || l.err != err.Error()) && !errors.Is(err, context.Canceled) {
		clog.Log.Warnf("%s: not answering: %v", l.cfg.Name, err)
	}

	l.up, l.err = false, err.Error()
	l.duties, l.dutyErr = map[int]int{}, map[int]error{}
	_ = l.relay.Close(ctx)
}

// fanAlarms says when a fan stops, or starts again. A fan with no reading at all is a header with nothing plugged in, and is not one.
func (l *link) fanAlarms() {
	for _, s := range l.sensors {
		if s.Kind != bmc.Fan || !s.Readable {
			continue
		}

		stopped := s.Value <= 0
		switch {
		case stopped && !l.stopped[s.Name]:
			clog.Log.Warnf("%s: %s has stopped", l.cfg.Name, s.Name)
		case !stopped && l.stopped[s.Name]:
			clog.Log.Infof("%s: %s is turning again, at %.0f rpm", l.cfg.Name, s.Name, s.Value)
		}
		l.stopped[s.Name] = stopped
	}
}

// fanStates is the BMC's fans as last read, each with its zone: the settings' list for a zone that has one, or the maker's rule.
func (l *link) fanStates(zones []config.Zone) []FanState {
	named := map[string]int{}
	for _, z := range zones {
		for _, f := range z.Fans {
			named[f] = z.Zone
		}
	}

	var out []FanState
	for _, s := range l.sensors {
		if s.Kind != bmc.Fan {
			continue
		}

		zone, ok := named[s.Name]
		if !ok {
			zone = l.fans.Zone(s.Name)
		}
		out = append(out, FanState{Name: s.Name, RPM: s.Value, Readable: s.Readable, Zone: zone, Stopped: s.Readable && s.Value <= 0})
	}

	return out
}

// take readies the BMC to have its zones set, and says when it had to, which a Supermicro board answers by running every fan at full speed: each
// zone is then set whatever it reads. Having to ready it again, after a reset say, is counted.
func (l *link) take(ctx context.Context) (bool, error) {
	again, err := l.fans.Take(ctx)
	if err != nil {
		return false, err
	}

	if again && l.taken {
		l.retakes++
		clog.Log.Warnf("%s: the BMC had taken its fans back, a reset perhaps: readied it again", l.cfg.Name)
	}
	l.taken = true

	return again, nil
}

// handBack leaves the BMC's fans as the settings say for when the program stops: each zone the program set put back as it was found and the BMC
// readied no more, or each at full speed. A BMC whose connection was dropped, by a round the stop cut short say, is dialed again for it. It then
// closes the connection.
func (l *link) handBack(ctx context.Context, dial Dial, seats map[int]*seat) error {
	defer l.relay.Close(ctx) //nolint:errcheck // closing is all that is left to do, and a BMC that will not is one the program is done with

	if !l.taken && !anySet(seats) {
		return nil
	}
	if _, err := l.relay.get(); err != nil {
		conn, err := dial(ctx, l.cfg)
		if err != nil {
			return fmt.Errorf("%s is not answering, so its fans stay as they are: %w", l.cfg.Name, err)
		}

		l.relay.mu.Lock()
		l.relay.conn = conn
		l.relay.mu.Unlock()
	}

	var errs []error
	for zone, s := range seats {
		if !s.setKnown {
			continue
		}

		switch {
		case l.cfg.OnStop == config.OnStopFull:
			errs = append(errs, l.fans.SetDuty(ctx, zone, 100))
		case s.foundKnown:
			errs = append(errs, l.fans.SetDuty(ctx, zone, s.found))
			clog.Log.Infof("%s: zone %d put back to the %d%% it was found at", l.cfg.Name, zone, s.found)
		}
	}

	if l.cfg.OnStop != config.OnStopFull && l.taken {
		errs = append(errs, l.fans.Give(ctx))
	}

	return errors.Join(errs...)
}

func anySet(seats map[int]*seat) bool {
	for _, s := range seats {
		if s.setKnown {
			return true
		}
	}

	return false
}

// seat is what the program knows of one zone of one BMC, kept across rounds and across the settings changing what the zone is called.
type seat struct {
	// found is how fast the zone was set when first read, before the program set it
	found      int
	foundKnown bool
	// set is what the program last set it to
	set      int
	setKnown bool
	// last is how fast the program last decided it should run, set or not, from which its curves come down
	last      int
	lastKnown bool

	sets, takeovers, resets int64
	corrections             []Correction
	state, why              string
}

// keepCorrections is how many of a zone's latest corrections are kept for the web page.
const keepCorrections = 100

// note keeps a correction, put in words: whether the zone had to be set, and whether setting it worked.
func (s *seat) note(fix *Correction, wrote bool, err error) {
	if fix == nil {
		return
	}

	if fix.Why == Reset {
		fix.Says = "its BMC had left the fan mode it was put in, a reset perhaps, and was readied again"
	} else {
		fix.Says = fmt.Sprintf("found at %d%%, not the %d%% it was set to: its BMC or someone else changed it", pointer.From(fix.Found), fix.Was)
	}

	switch {
	case err != nil:
		fix.Says += ", and setting it again failed: " + err.Error()
	case !wrote:
		fix.Says += fmt.Sprintf(", to the %d%% it wants now, so it was left there", fix.Set)
	default:
		fix.Says += fmt.Sprintf(", so it was set to %d%%", fix.Set)
	}

	s.corrections = append(s.corrections, *fix)
	if over := len(s.corrections) - keepCorrections; over > 0 {
		s.corrections = slices.Delete(s.corrections, 0, over)
	}
}

// seatKey names a zone of a BMC.
func seatKey(bmcName string, zone int) string {
	return bmcName + "/" + strconv.Itoa(zone)
}
