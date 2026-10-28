package supermicro_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/bmctest"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/supermicro"
)

// A shelf's zone is read and set with the commands tried on a real shelf, and nothing else is sent: its mode is never touched.
func TestAShelf(t *testing.T) {
	t.Parallel()

	board := bmctest.Shelf(60)
	fans := supermicro.New(board, bmc.Options{})

	if d, err := fans.Duty(t.Context(), 0); err != nil || d != 60 {
		t.Fatalf("zone 0 reads %d (%v), want 60", d, err)
	}
	if again, err := fans.Take(t.Context()); err != nil || again {
		t.Errorf("readying a shelf did something (%v, %v)", again, err)
	}
	if err := fans.SetDuty(t.Context(), 0, 45); err != nil {
		t.Fatal(err)
	}
	if err := fans.Give(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := board.Writes(); !slices.Equal(got, []string{"zone 0 45"}) {
		t.Errorf("sent %v, want only zone 0 set to 45", got)
	}
	if d, _ := fans.Duty(t.Context(), 0); d != 45 {
		t.Errorf("zone 0 reads %d after being set to 45", d)
	}
}

// A mainboard told to is put in Full mode, readied again after a reset, and put back in the mode it was found in.
func TestAMainboardInFullMode(t *testing.T) {
	t.Parallel()

	board := bmctest.Mainboard(50)
	fans := supermicro.New(board, bmc.Options{FullMode: true})

	if m, err := fans.Mode(t.Context()); err != nil || m != "standard (0)" {
		t.Fatalf("the mode is %q (%v)", m, err)
	}
	if again, err := fans.Take(t.Context()); err != nil || !again {
		t.Fatalf("readying it did nothing (%v)", err)
	}
	if again, _ := fans.Take(t.Context()); again {
		t.Error("readying it twice did something the second time")
	}

	board.Reset()
	if again, _ := fans.Take(t.Context()); !again {
		t.Error("it was not readied again after a reset")
	}
	if err := fans.Give(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := board.Writes(); !slices.Equal(got, []string{"mode 1", "mode 1", "mode 0"}) {
		t.Errorf("sent %v", got)
	}
}

// A duty that is not a percentage is refused before anything is sent.
func TestDutiesOutOfRange(t *testing.T) {
	t.Parallel()

	board := bmctest.Shelf(60)
	fans := supermicro.New(board, bmc.Options{})
	for _, d := range []int{-1, 101} {
		if err := fans.SetDuty(t.Context(), 0, d); err == nil {
			t.Errorf("%d%% was taken", d)
		}
	}
	if len(board.Writes()) != 0 {
		t.Errorf("sent %v", board.Writes())
	}
}

// A BMC that is not answering is an error that is not the BMC refusing.
func TestNotAnswering(t *testing.T) {
	t.Parallel()

	board := bmctest.Shelf(60)
	board.Down(true)

	_, err := supermicro.New(board, bmc.Options{}).Duty(t.Context(), 0)
	var refused *bmc.CompletionError
	if !errors.Is(err, bmctest.ErrDown) || errors.As(err, &refused) {
		t.Errorf("got %v", err)
	}
}

// Numbered fans are zone 0 and lettered ones zone 1.
func TestWhichZoneAFanIsIn(t *testing.T) {
	t.Parallel()

	fans := supermicro.New(bmctest.Shelf(60), bmc.Options{})
	for fan, want := range map[string]int{"FAN1": 0, "FAN10": 0, "FANA": 1, "fan b": 1, "CPU FAN": -1, "FAN": -1} {
		if got := fans.Zone(fan); got != want {
			t.Errorf("%s is in zone %d, want %d", fan, got, want)
		}
	}
}
