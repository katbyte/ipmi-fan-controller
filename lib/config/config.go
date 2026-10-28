// Package config is the program's settings file: the BMCs it talks to, the fan zones on them it sets, and the temperatures each zone follows. It is
// YAML, read at the start and again whenever the web page saves a change, and a change is written back into it with its comments kept.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// The defaults, for a setting the file leaves out. Temperatures are read every ten seconds, which is soon enough for a CPU, and disks every minute,
// since a disk warms over minutes and asking it costs it a command. A zone never runs under 30%, which every fan tried so far still turns at.
const (
	DefaultInterval     = 10 * time.Second
	DefaultDiskInterval = time.Minute
	DefaultHysteresis   = 2.0
	DefaultFloor        = 30
	DefaultCeiling      = 100
	DefaultFailsafe     = 100
	DefaultPort         = 623
	DefaultMaker        = "supermicro"
)

// What a BMC is told to do with its fans when the program stops.
const (
	// OnStopRestore puts each zone back at the speed it was found at, and the BMC back in the mode it was in.
	OnStopRestore = "restore"
	// OnStopFull leaves every zone at full speed.
	OnStopFull = "full"
)

// How a follow makes one temperature of the several it reads.
const (
	ByHottest = "hottest"
	ByAverage = "average"
)

// What the program does about disks the kernel gives no temperature for.
const (
	SmartctlAuto = "auto"
	SmartctlOff  = "off"
)

// Config is everything the settings file says, with the defaults in place of what it leaves out.
type Config struct {
	// Interval is how often temperatures are read and fans set.
	Interval Duration `yaml:"interval"`
	// DiskInterval is how often disks are asked their temperature.
	DiskInterval Duration `yaml:"disk_interval"`
	// Hysteresis is how far a temperature has to fall before the fans slow down, so they do not hunt up and down around one point.
	Hysteresis float64 `yaml:"hysteresis"`
	// Smartctl is what asks disks the kernel gives no temperature for: auto runs smartctl when it is there, off never does, and a path names it.
	Smartctl string `yaml:"smartctl"`

	BMCs  []BMC  `yaml:"bmcs"`
	Zones []Zone `yaml:"zones"`
}

// BMC is one BMC whose fans the program reads and sets: the machine's own, or one reached over the network.
type BMC struct {
	// Name is what the BMC is called on the page and in sensor names: tower, annex.
	Name string `yaml:"name"`
	// Maker is who made it, which decides how its fans are set: supermicro unless said.
	Maker string `yaml:"maker"`
	// Host is where it is on the network. Left out, it is the machine's own BMC, asked through Linux's IPMI driver at /dev/ipmi0.
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	// Password is the login's password, or ${NAME} for one in the environment; PasswordFile reads it from a file instead, a container's secret say.
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	// FullMode puts a Supermicro board in its Full fan mode while the program sets its fans, since a board in any other mode sets them itself as its
	// temperatures move. Without it a zone the BMC changes is set again the next round; whether Full holds a disk shelf's BMC is not yet tried.
	FullMode bool `yaml:"full_mode"`
	// OnStop is what is left on the fans when the program stops: restore or full.
	OnStop string `yaml:"on_stop"`
}

// Local says the BMC is the machine's own, not one on the network.
func (b BMC) Local() bool {
	return b.Host == ""
}

// Zone is one group of fans a BMC sets together, and what decides how fast they run.
type Zone struct {
	// Name is what the zone is called on the page: shelf, cpu.
	Name string `yaml:"name"`
	// BMC names the BMC the zone is on, and Zone is that BMC's number for it: Supermicro boards have 0 for the CPU fans and 1 for the rest.
	BMC  string `yaml:"bmc"`
	Zone int    `yaml:"zone"`
	// Floor and Ceiling are the slowest and fastest the zone is run at, as a percentage of full speed, whatever its curves say.
	Floor   int `yaml:"floor"`
	Ceiling int `yaml:"ceiling"`
	// Failsafe is how fast it runs when what it follows stops answering.
	Failsafe int `yaml:"failsafe"`
	// Fans names the fans in the zone, for the page. Left out, the maker's own rule says which they are.
	Fans []string `yaml:"fans"`
	// Follow is what decides the zone's speed: each asks for a speed from its own temperatures, and the zone runs at the fastest asked for.
	Follow []Follow `yaml:"follow"`
}

// UnmarshalYAML fills in the defaults before the file is read, so a setting the file leaves out keeps its default.
func (z *Zone) UnmarshalYAML(n *yaml.Node) error {
	type plain Zone
	p := plain{Floor: DefaultFloor, Ceiling: DefaultCeiling, Failsafe: DefaultFailsafe}
	if err := n.Decode(&p); err != nil {
		return err
	}
	*z = Zone(p)

	return nil
}

// Follow is one set of temperatures a zone follows, and the curve that turns them into a speed.
type Follow struct {
	// Name is what the follow is called on the page. Left out, it is its sensors.
	Name string `yaml:"name"`
	// Sensors are the readings followed, by name or with * for any part of one: disk/5003048000f8a93f/* is every disk in that shelf.
	Sensors []string `yaml:"sensors"`
	// By is how the readings make one temperature: hottest, unless average is said.
	By    string `yaml:"by"`
	Curve Curve  `yaml:"curve"`
}

// Label is what the follow is called: its name, or its sensors when it has none.
func (f Follow) Label() string {
	if f.Name != "" {
		return f.Name
	}

	return strings.Join(f.Sensors, ", ")
}

// Matches says whether a reading's name is one of the follow's sensors.
func (f Follow) Matches(id string) bool {
	for _, p := range f.Sensors {
		if ok, _ := path.Match(p, id); ok {
			return true
		}
	}

	return false
}

// Duration is a length of time as written in the file: 10s, 1m.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(strings.TrimSpace(n.Value))
	if err != nil {
		return fmt.Errorf("line %d: a length of time is written as 10s or 1m, and not %q", n.Line, n.Value)
	}
	*d = Duration(v)

	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

func (d Duration) String() string {
	return time.Duration(d).String()
}

// How YAML marks a whole number, and text.
const (
	intTag = "!!int"
	strTag = "!!str"
)

// Point is one point on a curve: at this temperature, this duty, as a percentage of full speed.
type Point struct {
	Celsius float64 `json:"celsius"`
	Duty    int     `json:"duty"`
}

// Curve is how fast a zone runs for a temperature, written as temperature: duty pairs, {35: 30, 45: 50, 55: 100}, with straight lines between them.
// Under its coolest point it asks for that point's duty, and over its hottest, that one's.
type Curve []Point

func (c *Curve) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a curve is written as temperature: duty pairs, {35: 30, 55: 100}", n.Line)
	}

	out := make(Curve, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		celsius, err := strconv.ParseFloat(k.Value, 64)
		if err != nil {
			return fmt.Errorf("line %d: a curve's temperature is a number of degrees, and not %q", k.Line, k.Value)
		}
		duty, err := strconv.Atoi(v.Value)
		if err != nil {
			return fmt.Errorf("line %d: a curve's duty is a whole percentage, and not %q", v.Line, v.Value)
		}

		out = append(out, Point{Celsius: celsius, Duty: duty})
	}

	slices.SortFunc(out, func(a, b Point) int { return compare(a.Celsius, b.Celsius) })
	*c = out

	return nil
}

func (c Curve) MarshalYAML() (any, error) {
	return c.Node(), nil
}

// Node is the curve as it is written in the file, on one line.
func (c Curve) Node() *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for _, p := range c {
		tag := intTag
		if p.Celsius != math.Trunc(p.Celsius) {
			tag = "!!float"
		}

		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: strconv.FormatFloat(p.Celsius, 'f', -1, 64)},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: intTag, Value: strconv.Itoa(p.Duty)},
		)
	}

	return n
}

// At is the duty the curve asks for at a temperature, before it is rounded.
func (c Curve) At(celsius float64) float64 {
	if len(c) == 0 {
		return 0
	}
	if celsius <= c[0].Celsius {
		return float64(c[0].Duty)
	}

	for i := 1; i < len(c); i++ {
		lo, hi := c[i-1], c[i]
		if celsius <= hi.Celsius {
			return float64(lo.Duty) + (celsius-lo.Celsius)*float64(hi.Duty-lo.Duty)/(hi.Celsius-lo.Celsius)
		}
	}

	return float64(c[len(c)-1].Duty)
}

// Top is the temperature at which the curve asks for its most.
func (c Curve) Top() float64 {
	if len(c) == 0 {
		return math.Inf(1)
	}

	return c[len(c)-1].Celsius
}

func compare(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Parse reads a settings file's text, fills in the defaults, puts in what each ${NAME} in the BMCs' logins stands for, and checks it all, naming
// every mistake at once. makers are the makers the program knows.
func Parse(text []byte, makers []string) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, err
	}

	c := &Config{Interval: Duration(DefaultInterval), DiskInterval: Duration(DefaultDiskInterval), Hysteresis: DefaultHysteresis, Smartctl: SmartctlAuto}
	if len(doc.Content) == 0 {
		return c, nil
	}

	if problems := unknown(&doc, typeOfConfig, ""); len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	if err := doc.Decode(c); err != nil {
		return nil, err
	}

	for i := range c.BMCs {
		b := &c.BMCs[i]
		b.Maker = strings.ToLower(cmpOr(b.Maker, DefaultMaker))
		b.OnStop = strings.ToLower(cmpOr(b.OnStop, OnStopRestore))
		if b.Port == 0 {
			b.Port = DefaultPort
		}
	}
	for i := range c.Zones {
		for j := range c.Zones[i].Follow {
			f := &c.Zones[i].Follow[j]
			f.By = strings.ToLower(cmpOr(f.By, ByHottest))
		}
	}

	problems := c.check(makers)
	problems = append(problems, c.expand()...)
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	return c, nil
}

func cmpOr(s, or string) string {
	if strings.TrimSpace(s) == "" {
		return or
	}

	return strings.TrimSpace(s)
}

// expand puts in what each ${NAME} in a BMC's address and login stands for, and reads a password from its file.
func (c *Config) expand() []error {
	var problems []error
	for i := range c.BMCs {
		b := &c.BMCs[i]
		where := "bmcs[" + strconv.Itoa(i) + "] (" + b.Name + ")"
		one := func(name, value string) string {
			var missing []string
			expanded := os.Expand(value, func(v string) string {
				found, ok := os.LookupEnv(v)
				if !ok {
					missing = append(missing, v)
				}

				return found
			})
			if len(missing) > 0 {
				problems = append(problems, fmt.Errorf("%s.%s: the environment has no %s", where, name, strings.Join(missing, " or ")))
			}

			return expanded
		}
		b.Host, b.User, b.Password, b.PasswordFile = one("host", b.Host), one("user", b.User), one("password", b.Password), one("password_file", b.PasswordFile)

		if b.PasswordFile != "" {
			raw, err := os.ReadFile(b.PasswordFile)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s.password_file: %w", where, err))
				continue
			}
			b.Password = strings.TrimRight(string(raw), "\r\n")
		}
	}

	return problems
}

// check finds every mistake in the settings, each said with where it is.
func (c *Config) check(makers []string) []error {
	var problems []error
	bad := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	if time.Duration(c.Interval) < time.Second {
		bad("interval: %s is shorter than a second", c.Interval)
	}
	if time.Duration(c.DiskInterval) < time.Duration(c.Interval) {
		bad("disk_interval: %s is shorter than interval (%s)", c.DiskInterval, c.Interval)
	}
	if c.Hysteresis < 0 || c.Hysteresis > 20 {
		bad("hysteresis: %g degrees is not between 0 and 20", c.Hysteresis)
	}

	bmcs := map[string]BMC{}
	local := ""
	for i, b := range c.BMCs {
		where := fmt.Sprintf("bmcs[%d] (%s)", i, b.Name)
		switch {
		case !nameOK(b.Name):
			bad("bmcs[%d].name: %q has to be letters, numbers, - and _, as it starts its sensors' names", i, b.Name)
		case bmcs[b.Name].Name != "":
			bad("%s.name: two BMCs are called %s", where, b.Name)
		}
		bmcs[b.Name] = b

		if !slices.Contains(makers, b.Maker) {
			bad("%s.maker: %q is not one this program knows: %s", where, b.Maker, strings.Join(makers, ", "))
		}
		if b.OnStop != OnStopRestore && b.OnStop != OnStopFull {
			bad("%s.on_stop: %q is neither %s nor %s", where, b.OnStop, OnStopRestore, OnStopFull)
		}
		if b.Port < 1 || b.Port > 65535 {
			bad("%s.port: %d is not a port", where, b.Port)
		}

		if b.Local() {
			if local != "" {
				bad("%s: %s and %s both have no host, and a machine has one BMC of its own", where, local, b.Name)
			}
			local = b.Name

			continue
		}
		if b.User == "" {
			bad("%s.user: a BMC on the network needs a login", where)
		}
		if b.Password == "" && b.PasswordFile == "" {
			bad("%s.password: a BMC on the network needs a password, or a password_file", where)
		}
	}

	zones := map[string]bool{}
	seats := map[string]string{}
	for i, z := range c.Zones {
		where := fmt.Sprintf("zones[%d] (%s)", i, z.Name)
		switch {
		case !nameOK(z.Name):
			bad("zones[%d].name: %q has to be letters, numbers, - and _", i, z.Name)
		case zones[z.Name]:
			bad("%s.name: two zones are called %s", where, z.Name)
		}
		zones[z.Name] = true

		if bmcs[z.BMC].Name == "" {
			bad("%s.bmc: %q is not one of the bmcs", where, z.BMC)
		}
		if z.Zone < 0 || z.Zone > 255 {
			bad("%s.zone: %d is not a zone number", where, z.Zone)
		}
		seat := z.BMC + "/" + strconv.Itoa(z.Zone)
		if other, taken := seats[seat]; taken {
			bad("%s: zone %d of %s is also set by %s", where, z.Zone, z.BMC, other)
		}
		seats[seat] = z.Name

		if z.Floor < 0 || z.Floor > z.Ceiling || z.Ceiling > 100 {
			bad("%s: floor (%d) and ceiling (%d) have to be percentages with the floor no higher than the ceiling", where, z.Floor, z.Ceiling)
		}
		if z.Failsafe < z.Floor || z.Failsafe > 100 {
			bad("%s.failsafe: %d is not between the floor (%d) and 100", where, z.Failsafe, z.Floor)
		}

		if len(z.Follow) == 0 {
			bad("%s.follow: a zone has to follow something", where)
		}
		for j, f := range z.Follow {
			problems = append(problems, f.check(fmt.Sprintf("%s.follow[%d]", where, j))...)
		}
	}

	return problems
}

// check finds the mistakes in one follow.
func (f Follow) check(where string) []error {
	var problems []error
	bad := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	if len(f.Sensors) == 0 {
		bad("%s.sensors: a follow has to name the sensors it follows", where)
	}
	for _, p := range f.Sensors {
		if _, err := path.Match(p, ""); err != nil {
			bad("%s.sensors: %q is not a pattern: %v", where, p, err)
		}
	}
	if f.By != ByHottest && f.By != ByAverage {
		bad("%s.by: %q is neither %s nor %s", where, f.By, ByHottest, ByAverage)
	}

	problems = append(problems, f.Curve.check(where+".curve")...)

	return problems
}

func (c Curve) check(where string) []error {
	if len(c) == 0 {
		return []error{fmt.Errorf("%s: a curve needs at least one temperature: duty pair", where)}
	}

	var problems []error
	for i, p := range c {
		if p.Celsius < -40 || p.Celsius > 150 {
			problems = append(problems, fmt.Errorf("%s: %g degrees is not a temperature a machine runs at", where, p.Celsius))
		}
		if p.Duty < 0 || p.Duty > 100 {
			problems = append(problems, fmt.Errorf("%s: %d%% at %g degrees is not a percentage", where, p.Duty, p.Celsius))
		}
		if i > 0 && p.Duty < c[i-1].Duty {
			problems = append(problems, fmt.Errorf("%s: %d%% at %g degrees is slower than %d%% at %g: a curve has to rise or stay level as it gets hotter", where, p.Duty, p.Celsius, c[i-1].Duty, c[i-1].Celsius))
		}
	}

	return problems
}

// nameOK says a name can go in a sensor's name and a URL as it is.
func nameOK(name string) bool {
	if name == "" {
		return false
	}

	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}

	return true
}

// Zone is the zone of that name, and whether there is one.
func (c *Config) Zone(name string) (Zone, bool) {
	for _, z := range c.Zones {
		if z.Name == name {
			return z, true
		}
	}

	return Zone{}, false
}
