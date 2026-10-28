// Package dell sets the fans of Dell servers through their iDRAC, with the commands people have long used for it. It has not been tried on a real
// iDRAC: it is here to be tried, and as the second maker that shows how one is added.
//
// An iDRAC sets every fan together, so it has the one zone, 0, and it does not say how fast that is set. Dell took the commands away in iDRAC 9
// firmware 3.34 and later, which refuse them.
package dell

import (
	"context"
	"fmt"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
)

// The commands, all under Dell's own function number and one command number.
const (
	netFn = 0x30
	cmd   = 0x30

	// control with manual or automatic hands the fans to whoever sets them, or back to the iDRAC
	control   = 0x01
	manual    = 0x00
	automatic = 0x01

	// duty with every and a percentage sets every fan
	duty  = 0x02
	every = 0xff
)

// manufacturer is Dell's number among the companies IANA keeps.
const manufacturer = 674

// Maker is Dell's way of setting fans, not yet tried.
var Maker = bmc.Maker{Name: "dell", Manufacturers: []uint32{manufacturer}, New: New}

// Fans is one iDRAC's fans.
type Fans struct {
	conn bmc.Conn
}

func New(conn bmc.Conn, _ bmc.Options) bmc.Fans {
	return &Fans{conn: conn}
}

// Mode is empty: an iDRAC does not say whether it has handed its fans over.
func (*Fans) Mode(context.Context) (string, error) {
	return "", nil
}

// Take hands the fans over each time it is asked, since an iDRAC that has reset takes them back without saying so.
func (f *Fans) Take(ctx context.Context) (bool, error) {
	if _, err := f.conn.Raw(ctx, netFn, cmd, control, manual); err != nil {
		return false, fmt.Errorf("taking the fans from the iDRAC: %w", err)
	}

	return false, nil
}

func (f *Fans) Give(ctx context.Context) error {
	if _, err := f.conn.Raw(ctx, netFn, cmd, control, automatic); err != nil {
		return fmt.Errorf("handing the fans back to the iDRAC: %w", err)
	}

	return nil
}

func (*Fans) Duty(context.Context, int) (int, error) {
	return 0, bmc.ErrUnreadable
}

func (f *Fans) SetDuty(ctx context.Context, zone, pct int) error {
	if zone != 0 {
		return fmt.Errorf("an iDRAC sets every fan together, as zone 0, and has no zone %d", zone)
	}
	if pct < 0 || pct > 100 {
		return fmt.Errorf("%d%% is not a duty", pct)
	}

	if _, err := f.conn.Raw(ctx, netFn, cmd, duty, every, byte(pct)); err != nil {
		return fmt.Errorf("setting the fans to %d%%: %w", pct, err)
	}

	return nil
}

// Zone is 0 for every fan.
func (*Fans) Zone(string) int {
	return 0
}
