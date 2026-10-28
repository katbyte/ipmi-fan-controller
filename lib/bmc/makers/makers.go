// Package makers is every maker whose BMCs the program can set fans on. A new one is a package beside supermicro and dell, and a line here.
package makers

import (
	"slices"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/dell"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/supermicro"
)

// all is every maker, the default first.
var all = []bmc.Maker{supermicro.Maker, dell.Maker}

// Get is the maker the settings call by that name.
func Get(name string) (bmc.Maker, bool) {
	for _, m := range all {
		if m.Name == name {
			return m, true
		}
	}

	return bmc.Maker{}, false
}

// Names is every maker's name, the default first.
func Names() []string {
	names := make([]string, 0, len(all))
	for _, m := range all {
		names = append(names, m.Name)
	}

	return names
}

// Of is the name of the maker whose BMCs give that number for who made them, or "".
func Of(manufacturer uint32) string {
	for _, m := range all {
		if slices.Contains(m.Manufacturers, manufacturer) {
			return m.Name
		}
	}

	return ""
}
