package dell_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/dell"
)

// idrac records the commands sent to it and answers each with nothing, as an iDRAC does.
type idrac struct {
	sent []string
}

func (i *idrac) Raw(_ context.Context, netFn, cmd byte, data ...byte) ([]byte, error) {
	i.sent = append(i.sent, fmt.Sprintf("% x", append([]byte{netFn, cmd}, data...)))

	return nil, nil
}

func (*idrac) Sensors(context.Context, bmc.Want) ([]bmc.Sensor, error) { return nil, nil }
func (*idrac) Device(context.Context) (bmc.Device, error)              { return bmc.Device{}, nil }
func (*idrac) Close(context.Context) error                             { return nil }

// The fans are taken, set and handed back with the commands people use, and their speed cannot be read back.
func TestAnIDRAC(t *testing.T) {
	t.Parallel()

	conn := &idrac{}
	fans := dell.New(conn, bmc.Options{})

	if _, err := fans.Take(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := fans.SetDuty(t.Context(), 0, 30); err != nil {
		t.Fatal(err)
	}
	if err := fans.SetDuty(t.Context(), 1, 30); err == nil {
		t.Error("zone 1 was set, and an iDRAC has only the one")
	}
	if err := fans.Give(t.Context()); err != nil {
		t.Fatal(err)
	}

	if want := []string{"30 30 01 00", "30 30 02 ff 1e", "30 30 01 01"}; !slices.Equal(conn.sent, want) {
		t.Errorf("sent %q, want %q", conn.sent, want)
	}
	if _, err := fans.Duty(t.Context(), 0); !errors.Is(err, bmc.ErrUnreadable) {
		t.Errorf("reading the duty gave %v", err)
	}
	if fans.Zone("Fan1 RPM") != 0 {
		t.Error("a fan is not in zone 0")
	}
}
