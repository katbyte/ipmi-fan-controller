package control

import (
	"math"
	"slices"
	"time"

	"github.com/katbyte/go-kt/pointer"
)

// keepHistory is how far back the web page's charts go: at a round every 10s, a couple of thousand rounds of each series, a few megabytes in all.
const keepHistory = 6 * time.Hour

// History is the rounds after one, column by column, as the web page charts them.
type History struct {
	// Rounds and At are each round's number and when it started, in milliseconds since 1970.
	Rounds []int64 `json:"rounds"`
	At     []int64 `json:"at"`
	// Series is each thing charted, by name, with its value at each round, null when it had none: duty/<zone> and want/<zone> in percent,
	// follow/<zone>/<follow> and temp/<reading> in °C, and fan/<bmc>/<fan> in rpm.
	Series map[string][]*float64 `json:"series"`
}

// history is what History is made from, kept column by column: each round's number and time, and each series' value at it, NaN where it had
// none.
type history struct {
	rounds []int64
	at     []time.Time
	series map[string][]float64
}

// add keeps one round's values, and drops the rounds older than keepHistory, and any series with nothing left in it.
func (h *history) add(round int64, at time.Time, values map[string]float64) {
	if h.series == nil {
		h.series = map[string][]float64{}
	}

	n := len(h.at)
	h.rounds = append(h.rounds, round)
	h.at = append(h.at, at)
	for name, col := range h.series {
		v, ok := values[name]
		if !ok {
			v = math.NaN()
		}
		h.series[name] = append(col, v)
	}
	for name, v := range values {
		if _, ok := h.series[name]; ok {
			continue
		}

		col := make([]float64, n, n+1)
		for i := range col {
			col[i] = math.NaN()
		}
		h.series[name] = append(col, v)
	}

	old := 0
	for old < len(h.at) && at.Sub(h.at[old]) > keepHistory {
		old++
	}
	if old == 0 {
		return
	}

	h.rounds, h.at = h.rounds[old:], h.at[old:]
	for name, col := range h.series {
		col = col[old:]
		if !slices.ContainsFunc(col, func(v float64) bool { return !math.IsNaN(v) }) {
			delete(h.series, name)
			continue
		}
		h.series[name] = col
	}
}

// since is the rounds after one: every one kept, for 0.
func (h *history) since(round int64) History {
	i, _ := slices.BinarySearch(h.rounds, round+1)

	out := History{Rounds: slices.Clone(h.rounds[i:]), At: make([]int64, 0, len(h.at)-i), Series: make(map[string][]*float64, len(h.series))}
	for _, t := range h.at[i:] {
		out.At = append(out.At, t.UnixMilli())
	}
	for name, col := range h.series {
		values := make([]*float64, len(col)-i)
		for j, v := range col[i:] {
			if !math.IsNaN(v) {
				values[j] = pointer.To(v)
			}
		}
		out.Series[name] = values
	}

	return out
}

// charted is a round's values as the web page charts them: each zone's speed and what it follows, each fan of a BMC that answered, and each
// temperature fresh enough to follow.
func charted(st State) map[string]float64 {
	values := map[string]float64{}

	for _, z := range st.Zones {
		if z.Duty != nil {
			values["duty/"+z.Name] = float64(*z.Duty)
		}
		values["want/"+z.Name] = float64(z.Want)
		for _, f := range z.Follows {
			if f.Celsius != nil {
				values["follow/"+z.Name+"/"+f.Name] = *f.Celsius
			}
		}
	}

	for _, b := range st.BMCs {
		if !b.Up {
			continue
		}
		for _, f := range b.Fans {
			if f.Readable {
				values["fan/"+b.Name+"/"+f.Name] = f.RPM
			}
		}
	}

	for _, r := range st.Readings {
		if r.Fresh {
			values["temp/"+r.ID] = r.Celsius
		}
	}

	return values
}

// History is what the web page charts, from the rounds after since: the last six hours for 0. It is kept only while the web page is on.
func (c *Controller) History(since int64) History {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return c.hist.since(since)
}
