package control

import (
	"time"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
)

// The states a zone can be in.
const (
	// Auto is a zone run by its curves.
	Auto = "auto"
	// Quiet is a zone held at a speed for a while, from the web page.
	Quiet = "quiet"
	// Full is a zone run at full speed, from the web page.
	Full = "full"
	// Failsafe is a zone run at its failsafe speed, since what it follows has stopped answering.
	Failsafe = "failsafe"
	// Unreachable is a zone on a BMC that is not answering, and so cannot be set.
	Unreachable = "unreachable"
)

// Why a zone was corrected: set again because it was not at what the program had set it to.
const (
	// Changed is a zone found at another speed than the program set it to: its BMC, or someone else, changed it.
	Changed = "changed"
	// Reset is a zone set again because its BMC had left the fan mode the program put it in, after a reset say.
	Reset = "reset"
)

// State is everything the program last saw and did, for the web page and status.
type State struct {
	// Started is when the first round started, At when the last one did, and Rounds how many there have been.
	Started time.Time `json:"started"`
	At      time.Time `json:"at"`
	Rounds  int64     `json:"rounds"`
	// Interval is how often rounds are run.
	Interval time.Duration `json:"interval"`
	// DryRun says nothing is being set: each zone says what it would be set to.
	DryRun bool `json:"dry_run"`
	// Probe is the test of which disks a zone cools, under way or last run, if there is one.
	Probe    *Probe         `json:"probe,omitempty"`
	Sources  []SourceState  `json:"sources"`
	BMCs     []BMCState     `json:"bmcs"`
	Zones    []ZoneState    `json:"zones"`
	Readings []ReadingState `json:"readings"`
}

// SourceState is how reading one source of temperatures is going: the machine's chips, its disks, or one BMC's sensors.
type SourceState struct {
	Name  string        `json:"name"`
	Every time.Duration `json:"every"`
	// OK says its last reading worked, and Fresh that it has worked recently enough to be followed.
	OK     bool      `json:"ok"`
	Fresh  bool      `json:"fresh"`
	LastOK time.Time `json:"last_ok"`
	Error  string    `json:"error,omitempty"`
	// Unread is each sensor it has that could not be read, and why.
	Unread map[string]string `json:"unread,omitempty"`
}

// BMCState is one BMC as last seen.
type BMCState struct {
	Name  string `json:"name"`
	Maker string `json:"maker"`
	// Tried says the maker's way of setting fans has been seen to work on a real BMC.
	Tried bool   `json:"tried"`
	Host  string `json:"host,omitempty"`
	Up    bool   `json:"up"`
	Error string `json:"error,omitempty"`
	// Device is who the BMC says made it, and MadeBy the maker that number is, when the program knows it.
	Device bmc.Device `json:"device"`
	MadeBy string     `json:"made_by,omitempty"`
	Mode   string     `json:"mode,omitempty"`
	Fans   []FanState `json:"fans"`
	// Retakes counts the times the BMC had to be readied again, after a reset say.
	Retakes int64 `json:"retakes"`
}

// FanState is one fan.
type FanState struct {
	Name     string  `json:"name"`
	RPM      float64 `json:"rpm"`
	Readable bool    `json:"readable"`
	// Zone is the zone the fan is in, or -1.
	Zone int `json:"zone"`
	// Stopped says the fan reads 0, which on a shelf whose BMC has no lower limit for it nothing else will say.
	Stopped bool `json:"stopped"`
}

// ZoneState is one zone: how fast it is, how fast it should be, and why.
type ZoneState struct {
	Name     string `json:"name"`
	BMC      string `json:"bmc"`
	Zone     int    `json:"zone"`
	Floor    int    `json:"floor"`
	Ceiling  int    `json:"ceiling"`
	Failsafe int    `json:"failsafe"`
	State    string `json:"state"`
	Why      string `json:"why"`
	// Duty is how fast the BMC says the zone is set, nil when it cannot say. Want is how fast it should be, and Set what the program last set.
	Duty *int `json:"duty"`
	Want int  `json:"want"`
	Set  *int `json:"set"`
	// Found is how fast it was when the program first read it, which it is put back to when the program stops.
	Found   *int          `json:"found"`
	Hold    *Hold         `json:"hold,omitempty"`
	Follows []FollowState `json:"follows"`
	Fans    []string      `json:"fans"`
	// Sets counts the times it was set, Takeovers the times it was found set to something else than the program set it to, and Resets the times it
	// was set again because its BMC had to be readied again.
	Sets      int64 `json:"sets"`
	Takeovers int64 `json:"takeovers"`
	Resets    int64 `json:"resets"`
	// Corrections is the latest of those, oldest first.
	Corrections []Correction `json:"corrections"`
}

// Correction is one time a zone was not at what the program had set it to, and what was done about it.
type Correction struct {
	At time.Time `json:"at"`
	// Why is Changed or Reset.
	Why string `json:"why"`
	// Found is the speed it was found at, nil when it could not be read; Was is what the program had set it to, and Set what it is set to now.
	Found *int `json:"found"`
	Was   int  `json:"was"`
	Set   int  `json:"set"`
	// Mode is the BMC's fan mode as last read, when it was.
	Mode string `json:"mode,omitempty"`
	// Says is all of it in words.
	Says string `json:"says"`
}

// FollowState is one follow of a zone, and what it asks for.
type FollowState struct {
	Name    string       `json:"name"`
	By      string       `json:"by"`
	Sensors []string     `json:"sensors"`
	Curve   config.Curve `json:"curve"`
	// Celsius is the temperature it follows, from Readings readings, the hottest of them Hottest.
	Celsius  *float64 `json:"celsius"`
	Hottest  string   `json:"hottest,omitempty"`
	Readings int      `json:"readings"`
	// Wants is the speed its curve asks for, before the floor and ceiling.
	Wants *int `json:"wants"`
	// Problem says why it is not being followed: it matches nothing, or a source it reads has stopped answering.
	Problem string `json:"problem,omitempty"`
}

// ReadingState is one temperature, where it came from, and which zones follow it.
type ReadingState struct {
	sensors.Reading

	Source string    `json:"source"`
	At     time.Time `json:"at"`
	Fresh  bool      `json:"fresh"`
	Zones  []string  `json:"zones,omitempty"`
}

// Hold is a zone held at one speed for a while from the web page: quiet, at a speed of its own, or full.
type Hold struct {
	Full bool `json:"full"`
	Duty int  `json:"duty"`
	// Until is when it ends, the zero time for never.
	Until time.Time `json:"until"`
}

// over says the hold has ended.
func (h Hold) over(now time.Time) bool {
	return !h.Until.IsZero() && !now.Before(h.Until)
}
