package makers_test

import (
	"testing"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc/makers"
)

// Supermicro comes first, as the default, and each maker is found by its name and by its number.
func TestTheMakers(t *testing.T) {
	t.Parallel()

	if names := makers.Names(); len(names) < 2 || names[0] != "supermicro" {
		t.Errorf("the makers are %v, want supermicro first", names)
	}
	if m, ok := makers.Get("supermicro"); !ok || !m.Tried {
		t.Errorf("supermicro is %+v (%v), want one that has been tried", m, ok)
	}
	if m, ok := makers.Get("dell"); !ok || m.Tried {
		t.Errorf("dell is %+v (%v), want one not yet tried", m, ok)
	}
	if _, ok := makers.Get("acme"); ok {
		t.Error("acme was found")
	}
	if makers.Of(10876) != "supermicro" || makers.Of(674) != "dell" || makers.Of(1) != "" {
		t.Error("the makers' numbers are wrong")
	}
}
