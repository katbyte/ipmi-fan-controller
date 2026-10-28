package control

import (
	"testing"

	"github.com/katbyte/ipmi-fan-controller/lib/config"
)

// A count is said again at its first, tenth, hundredth and thousandth time, and not between.
func TestLoud(t *testing.T) {
	t.Parallel()

	var said []int64
	for n := range int64(1001) {
		if loud(n + 1) {
			said = append(said, n+1)
		}
	}
	if len(said) != 4 || said[0] != 1 || said[1] != 10 || said[2] != 100 || said[3] != 1000 {
		t.Errorf("said at %v", said)
	}
}

// A Supermicro board not told to go into Full mode is told how to stop it taking a zone back; one that is, or another maker's, is not.
func TestTakeoverHint(t *testing.T) {
	t.Parallel()

	if hint := takeoverHint(config.BMC{Maker: "supermicro"}); len(hint) < 60 {
		t.Errorf("a mainboard without full_mode is hinted %q", hint)
	}
	if hint := takeoverHint(config.BMC{Maker: "supermicro", FullMode: true}); len(hint) > 60 {
		t.Errorf("a board in full_mode is hinted %q", hint)
	}
}
