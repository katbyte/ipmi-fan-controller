package control

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/katbyte/go-kt/clog"
)

// How long a test of which disks a zone cools may run: long enough for a disk to cool, which takes minutes, and no more than an hour flat out.
const (
	ProbeShortest = 5 * time.Minute
	ProbeLongest  = time.Hour
)

// Probe is a test of which disks a zone's fans cool: the zone runs flat out for a while, and each disk is compared with how hot it was when the
// test began.
type Probe struct {
	Zone    string    `json:"zone"`
	Started time.Time `json:"started"`
	Until   time.Time `json:"until"`
	// Done says the test is over, and Stopped that it was stopped before its time was up.
	Done    bool `json:"done"`
	Stopped bool `json:"stopped"`
	// Disks is every disk the program reads, from the first round of the test.
	Disks []ProbeDisk `json:"disks"`
}

// ProbeDisk is one disk in a test: how hot it was when the test began, and as last read.
type ProbeDisk struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Enclosure string  `json:"enclosure,omitempty"`
	Slot      string  `json:"slot,omitempty"`
	Before    float64 `json:"before"`
	Now       float64 `json:"now"`
}

// StartProbe runs a zone flat out for a while to see which disks it cools. One test runs at a time, or each would be cooling the other's disks, and
// none in a dry run, which sets nothing.
func (c *Controller) StartProbe(zone string, d time.Duration) error {
	if c.dryRun {
		return errors.New("a dry run sets nothing, so it cannot run a zone flat out to see which disks it cools")
	}
	if _, ok := c.Config().Zone(zone); !ok {
		return fmt.Errorf("there is no zone called %s", zone)
	}
	if d < ProbeShortest || d > ProbeLongest {
		return fmt.Errorf("a test runs for %v to %v, not %v", ProbeShortest, ProbeLongest, d)
	}

	now := c.now()
	until := now.Add(d)

	c.mu.Lock()
	if c.probe != nil && !c.probe.Done {
		c.mu.Unlock()
		return fmt.Errorf("zone %s is being tested already: one test at a time, or each would be cooling the other's disks", c.probe.Zone)
	}
	c.probe = &Probe{Zone: zone, Started: now, Until: until}
	c.holds[zone] = Hold{Full: true, Until: until}
	c.mu.Unlock()

	clog.Log.Infof("%s: testing which disks it cools: flat out until %s", zone, until.Format(time.TimeOnly))
	c.wake()

	return nil
}

// StopProbe ends a zone's test early, letting its fans go back to their curves, or puts away one that is over.
func (c *Controller) StopProbe(zone string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.probe == nil || c.probe.Zone != zone {
		return fmt.Errorf("zone %s is not being tested", zone)
	}
	if c.probe.Done {
		c.probe = nil
		return nil
	}

	c.probe.Done, c.probe.Stopped = true, true
	delete(c.holds, zone)
	clog.Log.Infof("%s: the test of which disks it cools was stopped", zone)
	c.wake()

	return nil
}

// probeRound notes each disk's temperature for a test under way, and how hot each was to begin with in its first round, read before the zone
// was run flat out. The test is over when its time is up, or stopped when its zone's hold is let go of.
func (c *Controller) probeRound(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	p := c.probe
	if p == nil || p.Done {
		return
	}

	if p.Disks == nil {
		p.Disks = []ProbeDisk{}
		for _, id := range slices.Sorted(maps.Keys(c.table)) {
			e := c.table[id]
			if d := e.reading.Disk; d != nil && c.sources[e.source].fresh(now) {
				p.Disks = append(p.Disks, ProbeDisk{ID: id, Name: d.Name, Enclosure: d.Enclosure, Slot: d.Slot, Before: e.reading.Celsius, Now: e.reading.Celsius})
			}
		}
	} else {
		for i, d := range p.Disks {
			if e, ok := c.table[d.ID]; ok && c.sources[e.source].fresh(now) {
				p.Disks[i].Now = e.reading.Celsius
			}
		}
	}

	_, held := c.holds[p.Zone]
	switch {
	case !now.Before(p.Until):
		p.Done = true
		clog.Log.Infof("%s: the test of which disks it cools is over: %s", p.Zone, p.verdict())
	case !held:
		p.Done, p.Stopped = true, true
		clog.Log.Infof("%s: its hold was let go of, so the test of which disks it cools was stopped", p.Zone)
	}
}

// verdict is the enclosure whose disks cooled the most on average, in words.
func (p *Probe) verdict() string {
	type cooled struct {
		enclosure string
		by        float64
		disks     int
	}

	var each []cooled
	for _, d := range p.Disks {
		i := slices.IndexFunc(each, func(c cooled) bool { return c.enclosure == d.Enclosure })
		if i < 0 {
			each = append(each, cooled{enclosure: d.Enclosure})
			i = len(each) - 1
		}
		each[i].by += d.Before - d.Now
		each[i].disks++
	}
	if len(each) == 0 {
		return "no disks were read"
	}

	best := slices.MaxFunc(each, func(a, b cooled) int { return cmp.Compare(a.by/float64(a.disks), b.by/float64(b.disks)) })
	where := "the disks in no enclosure"
	if best.enclosure != "" {
		where = "the disks in enclosure " + best.enclosure
	}

	return fmt.Sprintf("%s cooled most, by %.1f° on average", where, best.by/float64(best.disks))
}
