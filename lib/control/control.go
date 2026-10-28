// Package control is the program's work: each round it reads the temperatures, works out how fast each zone should run, sets it, and keeps what it
// saw and did for the web page and status. When it stops it leaves the fans as the settings say.
package control

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/makers"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
)

// staleAfter is how many of a source's intervals can pass without it answering before what it last said is no longer followed.
const staleAfter = 3

// stopLimit is how long the BMCs are given to have their fans handed back when the program stops.
const stopLimit = 30 * time.Second

// minRound is the least a round is given, however short the interval: long enough for a BMC on the network to be logged in to.
const minRound = 20 * time.Second

// The sources of temperatures that are not a BMC: the machine's chips and its disks. A BMC's is ipmi/ and its name.
const (
	sourceChips = "hwmon"
	sourceDisks = "disks"
)

// Machine is the Linux machine the program runs on, as far as its temperatures go: sensors.Machine, or a test's own.
type Machine interface {
	Chips(ctx context.Context, want sensors.Want) (sensors.Read, error)
	Disks(ctx context.Context, want sensors.Want) (sensors.Read, error)
}

// Options is what a controller is made with.
type Options struct {
	Config  *config.Config
	Machine Machine
	Dial    Dial
	// DryRun sets nothing: each zone says what it would be set to.
	DryRun bool
	// ReadAll reads every temperature, fan and fan mode there is, for the web page or status to show. Otherwise a round reads only what the zones
	// follow and what setting them needs.
	ReadAll bool
	// After is called at the end of each round that read and decided, with what it saw: serve marks the program alive with it.
	After func(State)
	// Now is the clock, time.Now unless a test says otherwise.
	Now func() time.Time
}

// Controller runs the rounds.
type Controller struct {
	machine Machine
	dial    Dial
	dryRun  bool
	readAll bool
	after   func(State)
	now     func() time.Time
	// kick runs a round at once, when a hold or a setting changes
	kick chan struct{}

	// mu keeps what the web page changes between rounds: the settings to take up next round, the holds, and the test of which disks a zone cools
	mu      sync.Mutex
	pending *config.Config
	current *config.Config
	holds   map[string]Hold
	probe   *Probe

	// rounding is held by a round, and keeps everything below it, which only a round reads or changes
	rounding sync.Mutex
	cfg      *config.Config
	links    map[string]*link
	seats    map[string]*seat
	sources  map[string]*source
	table    map[string]*entry
	started  time.Time
	rounds   int64
	disksDue time.Time
	stopped  bool

	// stateMu keeps what the last round saw, and the history of them the web page charts
	stateMu sync.RWMutex
	state   State
	hist    history
}

// source is one source of temperatures and how reading it goes.
type source struct {
	every  time.Duration
	ok     bool
	lastOK time.Time
	err    string
	unread map[string]string
}

// fresh says the source has answered recently enough for what it last said to be followed.
func (s *source) fresh(now time.Time) bool {
	return s != nil && !s.lastOK.IsZero() && now.Sub(s.lastOK) <= staleAfter*s.every
}

// New is a controller that has read nothing yet.
func New(o Options) *Controller {
	if o.Now == nil {
		o.Now = time.Now
	}

	return &Controller{
		machine: o.Machine,
		dial:    o.Dial,
		dryRun:  o.DryRun,
		readAll: o.ReadAll,
		after:   o.After,
		now:     o.Now,
		kick:    make(chan struct{}, 1),
		pending: o.Config,
		current: &config.Config{},
		holds:   map[string]Hold{},
		cfg:     &config.Config{},
		links:   map[string]*link{},
		seats:   map[string]*seat{},
		sources: map[string]*source{},
		table:   map[string]*entry{},
	}
}

// Config is the settings now in use, or about to be.
func (c *Controller) Config() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()

	return cmpOr(c.pending, c.current)
}

func cmpOr(a, b *config.Config) *config.Config {
	if a != nil {
		return a
	}

	return b
}

// Apply takes up new settings, at once.
func (c *Controller) Apply(cfg *config.Config) {
	c.mu.Lock()
	c.pending = cfg
	c.mu.Unlock()

	c.wake()
}

// Hold holds a zone at one speed for a while, or at full speed, at once.
func (c *Controller) Hold(zone string, h Hold) error {
	if _, ok := c.Config().Zone(zone); !ok {
		return fmt.Errorf("there is no zone called %s", zone)
	}
	if !h.Full && (h.Duty < 0 || h.Duty > 100) {
		return fmt.Errorf("%d%% is not a speed", h.Duty)
	}

	c.mu.Lock()
	c.holds[zone] = h
	c.mu.Unlock()

	clog.Log.Infof("%s: held%s", zone, describeHold(h))
	c.wake()

	return nil
}

// Release lets go of a zone's hold, at once.
func (c *Controller) Release(zone string) error {
	c.mu.Lock()
	_, held := c.holds[zone]
	delete(c.holds, zone)
	c.mu.Unlock()

	if !held {
		return fmt.Errorf("%s is not held", zone)
	}

	clog.Log.Infof("%s: let go", zone)
	c.wake()

	return nil
}

func describeHold(h Hold) string {
	if h.Full {
		return " at full speed" + until(&h)
	}

	return fmt.Sprintf(" quiet at %d%%%s", h.Duty, until(&h))
}

func (c *Controller) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// State is what the last round saw and did.
func (c *Controller) State() State {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return c.state
}

// Run runs a round every interval, and at once when a hold or a setting changes, until the context ends; then it hands the fans back.
func (c *Controller) Run(ctx context.Context) {
	for {
		c.Round(ctx)

		c.rounding.Lock()
		every := time.Duration(c.cfg.Interval)
		c.rounding.Unlock()

		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()

			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopLimit)
			c.Stop(stopCtx)
			cancel()

			return
		case <-t.C:
		case <-c.kick:
			t.Stop()
		}
	}
}

// Round reads everything, decides each zone, and sets each zone that needs it, unless this is a dry run.
func (c *Controller) Round(ctx context.Context) {
	c.rounding.Lock()
	defer c.rounding.Unlock()

	if c.stopped || ctx.Err() != nil {
		return
	}

	now := c.now()
	if c.rounds == 0 {
		c.started = now
	}
	c.rounds++
	c.takeUp(ctx)
	holds := c.currentHolds(now)

	// a BMC that stops answering mid-round is given up on before the next round is due, so it cannot hold up the zones on the others
	stopping := ctx
	ctx, cancel := context.WithTimeout(ctx, max(2*time.Duration(c.cfg.Interval), minRound))
	defer cancel()

	c.read(ctx, now)

	// a round the program's stopping cut short read only part of what it asked: nothing is decided or set from it
	if stopping.Err() != nil {
		return
	}
	c.probeRound(now)

	decided := map[string]decision{}
	followedBy := map[string][]followed{}
	fresh := func(name string) bool { return c.sources[name].fresh(now) }
	for _, z := range c.cfg.Zones {
		fs := make([]followed, 0, len(z.Follow))
		for _, f := range z.Follow {
			fs = append(fs, follow(f, c.table, fresh, c.cfg.Hysteresis))
		}
		followedBy[z.Name] = fs

		// a zone starts from its curve, not from wherever its BMC had it, and comes down from there by the hysteresis
		s := c.seat(z)
		var last *int
		if s.lastKnown {
			last = &s.last
		}

		var hold *Hold
		if h, ok := holds[z.Name]; ok {
			hold = &h
		}

		d := decide(z, fs, last, hold)
		decided[z.Name] = d
		s.last, s.lastKnown = d.want, true
	}

	c.set(ctx, now, decided)
	c.publish(now, decided, followedBy, holds)

	if c.after != nil {
		c.after(c.State())
	}
}

// wanted is the readings a round reads: every one when the page is to show them, and otherwise only those a zone follows.
func (c *Controller) wanted() sensors.Want {
	if c.readAll {
		return nil
	}

	return func(id string) bool {
		for _, z := range c.cfg.Zones {
			if slices.ContainsFunc(z.Follow, func(f config.Follow) bool { return f.Matches(id) }) {
				return true
			}
		}

		return false
	}
}

// followsAny says whether any zone could follow a reading whose name starts with these parts, hwmon say, or ipmi and a BMC's name, so a source no
// zone follows is not read at all.
func (c *Controller) followsAny(parts ...string) bool {
	if c.readAll {
		return true
	}

	for _, z := range c.cfg.Zones {
		for _, f := range z.Follow {
			for _, p := range f.Sensors {
				if startsLike(p, parts) {
					return true
				}
			}
		}
	}

	return false
}

// startsLike says whether a pattern's first parts could match these.
func startsLike(pattern string, parts []string) bool {
	given := strings.Split(pattern, "/")
	if len(given) < len(parts) {
		return false
	}

	for i, part := range parts {
		if ok, _ := path.Match(given[i], part); !ok {
			return false
		}
	}

	return true
}

// takeUp takes up settings the web page has saved: a BMC that is gone or has changed is handed back and dropped, as is a zone no longer set, and
// a new BMC is added, to be dialed this round.
func (c *Controller) takeUp(ctx context.Context) {
	c.mu.Lock()
	next := c.pending
	if next != nil {
		c.pending, c.current = nil, next
	}
	c.mu.Unlock()

	if next == nil {
		return
	}

	old := c.cfg
	c.cfg = next

	kept := map[string]bool{}
	for _, z := range next.Zones {
		kept[seatKey(z.BMC, z.Zone)] = true

		// a zone whose settings changed starts again from its curve, so a curve changed on the web page is seen at once
		if i := slices.IndexFunc(old.Zones, func(o config.Zone) bool { return o.Name == z.Name }); i < 0 || !reflect.DeepEqual(old.Zones[i], z) {
			if s := c.seats[seatKey(z.BMC, z.Zone)]; s != nil {
				s.lastKnown = false
			}
		}
	}

	for name, l := range c.links {
		i := slices.IndexFunc(next.BMCs, func(b config.BMC) bool { return b.Name == name })
		if i >= 0 && next.BMCs[i] == l.cfg {
			continue
		}

		if err := l.handBack(ctx, c.dial, c.seatsOf(name, nil)); err != nil {
			clog.Log.Warnf("%s: handing its fans back: %v", name, err)
		}
		for key := range c.seatsOf(name, nil) {
			delete(c.seats, seatKey(name, key))
		}
		delete(c.links, name)
		delete(c.sources, "ipmi/"+name)
	}

	// a zone no longer set by the settings is put back as it was found, on a BMC that is still there
	for key, s := range c.seats {
		if kept[key] || !s.setKnown {
			continue
		}

		name, zone := splitSeat(key)
		if l := c.links[name]; l != nil && s.foundKnown && !c.dryRun {
			if err := l.fans.SetDuty(ctx, zone, s.found); err != nil {
				clog.Log.Warnf("%s: putting zone %d back to %d%%: %v", name, zone, s.found, err)
			}
		}
		delete(c.seats, key)
	}

	for _, b := range next.BMCs {
		if c.links[b.Name] != nil {
			continue
		}

		l, err := newLink(b)
		if err != nil {
			clog.Log.Errorf("%v", err)
			continue
		}
		c.links[b.Name] = l
	}

	if old.Interval != next.Interval || old.DiskInterval != next.DiskInterval {
		c.disksDue = time.Time{}
	}
}

// currentHolds is the holds that have not ended, with those that have let go of.
func (c *Controller) currentHolds(now time.Time) map[string]Hold {
	c.mu.Lock()
	defer c.mu.Unlock()

	for zone, h := range c.holds {
		if h.over(now) {
			delete(c.holds, zone)
			clog.Log.Infof("%s: its hold has ended", zone)
		}
	}

	return maps.Clone(c.holds)
}

// read reads every source at once: the machine's chips each round, its disks when they are due, and every BMC.
func (c *Controller) read(ctx context.Context, now time.Time) {
	every := time.Duration(c.cfg.Interval)

	type result struct {
		name     string
		every    time.Duration
		readings []sensors.Reading
		unread   map[string]string
		err      error
	}

	var mu sync.Mutex
	var results []result
	add := func(r result) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}

	want := c.wanted()

	var wg sync.WaitGroup
	if c.machine != nil && c.followsAny("hwmon") {
		wg.Go(func() {
			r, err := c.machine.Chips(ctx, want)
			add(result{name: sourceChips, every: every, readings: r.Readings, unread: r.Unread, err: err})
		})
	}
	if c.machine != nil && c.followsAny("disk") && !now.Before(c.disksDue) {
		c.disksDue = now.Add(time.Duration(c.cfg.DiskInterval))
		wg.Go(func() {
			r, err := c.machine.Disks(ctx, want)
			add(result{name: sourceDisks, every: time.Duration(c.cfg.DiskInterval), readings: r.Readings, unread: r.Unread, err: err})
		})
	}

	for _, b := range c.cfg.BMCs {
		l := c.links[b.Name]
		if l == nil {
			continue
		}

		var zones []int
		for _, z := range c.cfg.Zones {
			if z.BMC == b.Name {
				zones = append(zones, z.Zone)
			}
		}

		ask := asking{sensors: c.followsAny("ipmi", b.Name), mode: c.readAll}
		if !c.readAll {
			ask.want = func(name string, kind bmc.Kind) bool {
				return kind == bmc.Temperature && want("ipmi/"+b.Name+"/"+strings.ReplaceAll(name, "/", "_"))
			}
		}

		wg.Go(func() {
			readings, unread := l.read(ctx, c.dial, zones, ask)
			var err error
			if !l.up {
				err = errors.New(l.err)
			}
			add(result{name: "ipmi/" + b.Name, every: every, readings: readings, unread: unread, err: err})
		})
	}
	wg.Wait()

	for _, r := range results {
		s := c.sources[r.name]
		if s == nil {
			s = &source{}
			c.sources[r.name] = s
		}
		s.every = r.every

		if r.err != nil {
			if s.ok || s.err != r.err.Error() {
				clog.Log.Warnf("reading %s: %v", r.name, r.err)
			}
			s.ok, s.err = false, r.err.Error()

			continue
		}

		if !s.ok && s.err != "" {
			clog.Log.Infof("reading %s again", r.name)
		}
		if changed(s.unread, r.unread) && len(r.unread) > 0 {
			clog.Log.Warnf("%s: %d sensors could not be read, %s", r.name, len(r.unread), firstUnread(r.unread))
		}
		s.ok, s.err, s.lastOK, s.unread = true, "", now, r.unread

		for id, e := range c.table {
			if e.source == r.name {
				delete(c.table, id)
			}
		}
		for _, reading := range r.readings {
			c.table[reading.ID] = &entry{reading: reading, source: r.name, at: now}
		}
	}

	// a BMC no longer in the settings takes its readings with it
	for id, e := range c.table {
		if _, ok := c.sources[e.source]; !ok {
			delete(c.table, id)
		}
	}
}

func changed(a, b map[string]string) bool {
	return !maps.Equal(a, b)
}

// firstUnread is the first sensor that could not be read, and why, for the log.
func firstUnread(unread map[string]string) string {
	first := slices.Sorted(maps.Keys(unread))[0]

	return first + " first: " + unread[first]
}

// seat is what the program knows of a zone of a BMC, made the first time it is asked for.
func (c *Controller) seat(z config.Zone) *seat {
	key := seatKey(z.BMC, z.Zone)
	s := c.seats[key]
	if s == nil {
		s = &seat{}
		c.seats[key] = s
	}

	return s
}

// seatsOf is what the program knows of each zone of a BMC, by zone number, the ones in zones only when it is given.
func (c *Controller) seatsOf(name string, zones []config.Zone) map[int]*seat {
	out := map[int]*seat{}
	for key, s := range c.seats {
		n, zone := splitSeat(key)
		if n != name {
			continue
		}
		if zones != nil && !slices.ContainsFunc(zones, func(z config.Zone) bool { return z.Zone == zone }) {
			continue
		}

		out[zone] = s
	}

	return out
}

func splitSeat(key string) (name string, zone int) {
	i := strings.LastIndex(key, "/")
	_, _ = fmt.Sscan(key[i+1:], &zone)

	return key[:i], zone
}

// set sets each zone that needs it, every BMC at once: one whose BMC set it to something else is counted and set again, and one at the speed
// wanted is left alone. Nothing is set in a dry run.
func (c *Controller) set(ctx context.Context, now time.Time, decided map[string]decision) {
	var wg sync.WaitGroup
	for name, l := range c.links {
		var zones []config.Zone
		for _, z := range c.cfg.Zones {
			if z.BMC == name {
				zones = append(zones, z)
			}
		}
		if len(zones) == 0 {
			continue
		}

		seats := map[int]*seat{}
		for _, z := range zones {
			s := c.seat(z)
			seats[z.Zone] = s

			// what the zone was at before the program first set it is kept, to be put back
			if d, ok := l.duties[z.Zone]; ok && !s.foundKnown {
				s.found, s.foundKnown = d, true
			}

			d := decided[z.Name]
			if s.state != d.state {
				clog.Log.Infof("%s: %s, %s", z.Name, d.state, d.why)
			}
			s.state, s.why = d.state, d.why
		}

		if c.dryRun || !l.up {
			continue
		}

		wg.Go(func() { setOn(ctx, now, l, zones, seats, decided) })
	}
	wg.Wait()
}

// setOn readies one BMC and sets its zones, noting each zone it has to correct.
func setOn(ctx context.Context, now time.Time, l *link, zones []config.Zone, seats map[int]*seat, decided map[string]decision) {
	retakes := l.retakes
	readied, err := l.take(ctx)
	if err != nil {
		clog.Log.Warnf("%s: readying it to have its fans set: %v", l.cfg.Name, err)
		if !refused(err) {
			l.down(ctx, err)
		}

		return
	}
	if readied {
		if mode, err := l.fans.Mode(ctx); err == nil {
			l.mode = mode
		}
	}

	for _, z := range zones {
		s, d := seats[z.Zone], decided[z.Name]
		duty, readable := l.duties[z.Zone]

		// a zone not at what the program set it to is corrected, and noted: its BMC was readied again, or it was changed. A zone read before its BMC
		// was readied may have changed with the readying, which is not someone else changing it.
		var fix *Correction
		switch {
		case l.retakes != retakes && s.setKnown:
			s.resets++
			fix = &Correction{At: now, Why: Reset, Was: s.set, Set: d.want}
			if readable {
				fix.Found = &duty
			}
		case readable && !readied && s.setKnown && duty != s.set:
			s.takeovers++
			fix = &Correction{At: now, Why: Changed, Found: &duty, Was: s.set, Set: d.want, Mode: l.mode}
			if loud(s.takeovers) {
				clog.Log.Warnf("%s: found at %d%%, not the %d%% it was set to, so it is set again (%d times so far)%s", z.Name, duty, s.set, s.takeovers, takeoverHint(l.cfg))
			}
		}

		if readable && !readied && duty == d.want {
			s.set, s.setKnown = d.want, true
			s.note(fix, false, nil)

			continue
		}

		err := l.fans.SetDuty(ctx, z.Zone, d.want)
		s.note(fix, true, err)
		if err != nil {
			clog.Log.Warnf("%s: %v", z.Name, err)
			if !refused(err) {
				l.down(ctx, err)
				return
			}

			continue
		}

		if !s.setKnown || s.set != d.want {
			clog.Log.Infof("%s: set to %d%%: %s", z.Name, d.want, d.why)
		}
		s.set, s.setKnown = d.want, true
		s.sets++
		if readable {
			l.duties[z.Zone] = d.want
		}
	}
}

// loud says a count is worth saying again: its first time, its tenth, its hundredth, and on, so a BMC that takes a zone back every round is not
// logged every round.
func loud(n int64) bool {
	for n > 1 && n%10 == 0 {
		n /= 10
	}

	return n == 1
}

// takeoverHint is what may stop a BMC taking a zone back.
func takeoverHint(b config.BMC) string {
	if b.Maker == config.DefaultMaker && !b.FullMode {
		return ": the BMC or someone else changed it. A Supermicro board sets its own fans as its temperatures move in any mode but Full: full_mode: true puts it in Full, not yet tried on a disk shelf"
	}

	return ": the BMC or someone else changed it"
}

// Stop hands every BMC's fans back as the settings say, and runs no more rounds.
func (c *Controller) Stop(ctx context.Context) {
	c.rounding.Lock()
	defer c.rounding.Unlock()

	if c.stopped {
		return
	}
	c.stopped = true

	var wg sync.WaitGroup
	for name, l := range c.links {
		seats := c.seatsOf(name, nil)
		wg.Go(func() {
			if err := l.handBack(ctx, c.dial, seats); err != nil {
				clog.Log.Warnf("%s: handing its fans back: %v", name, err)
			}
		})
	}
	wg.Wait()
}

// publish keeps what the round saw and did, for the pages.
func (c *Controller) publish(now time.Time, decided map[string]decision, followedBy map[string][]followed, holds map[string]Hold) {
	st := State{Started: c.started, At: now, Rounds: c.rounds, Interval: time.Duration(c.cfg.Interval), DryRun: c.dryRun}

	c.mu.Lock()
	if c.probe != nil {
		p := *c.probe
		p.Disks = slices.Clone(p.Disks)
		st.Probe = &p
	}
	c.mu.Unlock()

	for _, name := range slices.Sorted(maps.Keys(c.sources)) {
		s := c.sources[name]
		st.Sources = append(st.Sources, SourceState{Name: name, Every: s.every, OK: s.ok, Fresh: s.fresh(now), LastOK: s.lastOK, Error: s.err, Unread: s.unread})
	}

	for _, b := range c.cfg.BMCs {
		l := c.links[b.Name]
		if l == nil {
			continue
		}

		var zones []config.Zone
		for _, z := range c.cfg.Zones {
			if z.BMC == b.Name {
				zones = append(zones, z)
			}
		}

		st.BMCs = append(st.BMCs, BMCState{
			Name: b.Name, Maker: l.maker.Name, Tried: l.maker.Tried, Host: b.Host, Up: l.up, Error: l.err,
			Device: l.device, MadeBy: madeBy(l), Mode: l.mode, Fans: l.fanStates(zones), Retakes: l.retakes,
		})
	}

	for _, z := range c.cfg.Zones {
		s, d := c.seat(z), decided[z.Name]
		zs := ZoneState{Name: z.Name, BMC: z.BMC, Zone: z.Zone, Floor: z.Floor, Ceiling: z.Ceiling, Failsafe: z.Failsafe, State: d.state, Why: d.why, Want: d.want, Sets: s.sets, Takeovers: s.takeovers, Resets: s.resets, Corrections: slices.Clone(s.corrections)}

		l := c.links[z.BMC]
		switch {
		case l == nil || !l.up:
			zs.State, zs.Why = Unreachable, "its BMC is not answering, so it cannot be set: it stays where it was"
		case c.dryRun:
			zs.Why = fmt.Sprintf("would be set to %d%%: %s", d.want, d.why)
		}
		if l != nil {
			if duty, ok := l.duties[z.Zone]; ok {
				zs.Duty = &duty
			}
			for _, f := range l.fanStates([]config.Zone{z}) {
				if f.Zone == z.Zone {
					zs.Fans = append(zs.Fans, f.Name)
				}
			}
		}
		if s.setKnown {
			zs.Set = &s.set
		}
		if s.foundKnown {
			zs.Found = &s.found
		}
		if h, ok := holds[z.Name]; ok {
			zs.Hold = &h
		}
		for _, f := range followedBy[z.Name] {
			zs.Follows = append(zs.Follows, f.state)
		}

		st.Zones = append(st.Zones, zs)
	}

	for _, id := range slices.Sorted(maps.Keys(c.table)) {
		e := c.table[id]
		rs := ReadingState{Reading: e.reading, Source: e.source, At: e.at, Fresh: c.sources[e.source].fresh(now)}
		for _, z := range c.cfg.Zones {
			if slices.ContainsFunc(z.Follow, func(f config.Follow) bool { return f.Matches(id) }) {
				rs.Zones = append(rs.Zones, z.Name)
			}
		}
		st.Readings = append(st.Readings, rs)
	}

	c.stateMu.Lock()
	c.state = st
	if c.readAll {
		c.hist.add(c.rounds, now, charted(st))
	}
	c.stateMu.Unlock()
}

// madeBy is the maker the BMC's own number says made it, when the program knows it.
func madeBy(l *link) string {
	if l.device.Manufacturer == 0 {
		return ""
	}

	return makers.Of(l.device.Manufacturer)
}
