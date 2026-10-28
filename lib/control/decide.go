package control

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
)

// entry is one reading as last read, and where and when it was read.
type entry struct {
	reading sensors.Reading
	source  string
	at      time.Time
}

// followed is one follow as read this round: what it asks for going up and coming down, or the problem that stops it being followed.
type followed struct {
	state    FollowState
	up, down float64
	failed   bool
}

// follow reads one follow from the readings: the temperature its sensors make, and the speed its curve asks for at it, and at that temperature
// plus the hysteresis, which is the slowest it may come down to. A sensor whose source has stopped answering fails the follow, since what it would
// say is not known; a sensor that is simply gone, a disk pulled say, is left out.
func follow(f config.Follow, table map[string]*entry, fresh func(source string) bool, hysteresis float64) followed {
	out := followed{state: FollowState{Name: f.Label(), By: f.By, Sensors: f.Sensors, Curve: f.Curve}}

	var temps []float64
	var stale []string
	hottest, hot := "", math.Inf(-1)
	for _, id := range slices.Sorted(maps.Keys(table)) {
		if !f.Matches(id) {
			continue
		}

		e := table[id]
		if !fresh(e.source) {
			if !slices.Contains(stale, e.source) {
				stale = append(stale, e.source)
			}

			continue
		}

		temps = append(temps, e.reading.Celsius)
		if e.reading.Celsius > hot {
			hottest, hot = id, e.reading.Celsius
		}
	}

	switch {
	case len(stale) > 0:
		out.failed = true
		out.state.Problem = strings.Join(stale, " and ") + " stopped answering"

		return out
	case len(temps) == 0:
		out.failed = true
		out.state.Problem = "no sensor matches " + strings.Join(f.Sensors, " or ")

		return out
	}

	t := hot
	if f.By == config.ByAverage {
		sum := 0.0
		for _, v := range temps {
			sum += v
		}
		t = sum / float64(len(temps))
	}

	out.up, out.down = f.Curve.At(t), f.Curve.At(t+hysteresis)
	wants := int(math.Ceil(out.up))
	out.state.Celsius, out.state.Hottest, out.state.Readings, out.state.Wants = &t, hottest, len(temps), &wants

	return out
}

// decision is how fast a zone should run and why.
type decision struct {
	want  int
	state string
	why   string
}

// decide is how fast a zone should run, from its follows, how fast it last should have, and any hold on it.
//
// Its curves are run upwards at once and downwards only once the temperature has fallen by the hysteresis, so the fans do not hunt around one
// point. A follow that cannot be followed runs the zone at its failsafe speed; a full hold runs it at full speed whatever else; a quiet hold runs
// it at the hold's speed until a temperature reaches the top of its curve.
func decide(z config.Zone, follows []followed, last *int, hold *Hold) decision {
	if hold != nil && hold.Full {
		return decision{want: 100, state: Full, why: "held at full speed" + until(hold)}
	}

	failed := []string{}
	if len(follows) == 0 {
		failed = append(failed, "it follows nothing")
	}
	for _, f := range follows {
		if f.failed {
			failed = append(failed, f.state.Name+": "+f.state.Problem)
		}
	}
	if len(failed) > 0 {
		return decision{want: z.Failsafe, state: Failsafe, why: fmt.Sprintf("at the failsafe speed of %d%%, since %s", z.Failsafe, strings.Join(failed, "; "))}
	}

	up, down, leader := 0.0, 0.0, followed{}
	for _, f := range follows {
		if f.up > up || leader.state.Name == "" {
			up, leader = f.up, f
		}
		down = max(down, f.down)
	}

	if hold != nil {
		for _, f := range follows {
			if f.state.Celsius != nil && *f.state.Celsius >= f.state.Curve.Top() {
				return clamp(z, autoWant(up, down, last), Auto, fmt.Sprintf("too hot to stay quiet: %s is at %.1f°, the top of its curve", f.state.Name, *f.state.Celsius))
			}
		}

		return decision{want: max(hold.Duty, z.Floor), state: Quiet, why: fmt.Sprintf("held quiet at %d%%", max(hold.Duty, z.Floor)) + until(hold)}
	}

	want := autoWant(up, down, last)
	celsius := pointer.From(leader.state.Celsius)
	why := fmt.Sprintf("%s at %.1f° asks for %d%%", leader.state.Name, celsius, int(math.Ceil(up)))
	if leader.state.Readings > 1 && leader.state.By == config.ByHottest {
		why = fmt.Sprintf("%s at %.1f° (%s, the hottest of %d) asks for %d%%", leader.state.Name, celsius, leader.state.Hottest, leader.state.Readings, int(math.Ceil(up)))
	}
	if want > int(math.Ceil(up)) {
		why += fmt.Sprintf(", kept at %d%% until it has cooled further", want)
	}

	return clamp(z, want, Auto, why)
}

// autoWant is the speed the curves ask for: up to what they ask at once, and down only as far as the temperature plus the hysteresis allows.
func autoWant(up, down float64, last *int) int {
	rise, fall := int(math.Ceil(up)), int(math.Ceil(down))
	switch {
	case last == nil, rise >= *last:
		return rise
	case fall < *last:
		return fall
	default:
		return *last
	}
}

// clamp keeps a zone's speed between its floor and ceiling, and says so when it had to.
func clamp(z config.Zone, want int, state, why string) decision {
	switch {
	case want < z.Floor:
		return decision{want: z.Floor, state: state, why: fmt.Sprintf("%s, held to the floor of %d%%", why, z.Floor)}
	case want > z.Ceiling:
		return decision{want: z.Ceiling, state: state, why: fmt.Sprintf("%s, held to the ceiling of %d%%", why, z.Ceiling)}
	default:
		return decision{want: want, state: state, why: why}
	}
}

// until says when a hold ends.
func until(h *Hold) string {
	if h.Until.IsZero() {
		return " until let go"
	}

	return " until " + h.Until.Format("15:04")
}
