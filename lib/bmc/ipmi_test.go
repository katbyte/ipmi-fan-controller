package bmc_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/bmctest"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/supermicro"
)

// The program's own IPMI, against a made-up shelf on the network speaking IPMI 2.0 as a real BMC does: it logs in, reads who made the BMC and every
// sensor, tells a fan header with nothing on it from a stopped fan, and reads and sets a zone with Supermicro's commands, all over the wire.
func TestOverTheNetwork(t *testing.T) {
	t.Parallel()

	board := bmctest.Shelf(60).
		Sensor("System Temp", bmc.Temperature, 31).
		Sensor("SAS3 F1 Temp", bmc.Temperature, 68).
		Sensor("FAN1", bmc.Fan, 4500).
		Sensor("FAN2", bmc.Fan, 0).
		Unreadable("FAN8", bmc.Fan)
	host, port := board.Serve(t, "127.0.0.1:0", "hunter2")

	conn, err := bmc.Dial(t.Context(), bmc.Address{Host: host, Port: port, User: bmctest.User, Password: "hunter2", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(t.Context()) })

	device, err := conn.Device(t.Context())
	if err != nil || device.Manufacturer != 10876 || device.Product != 0xffff {
		t.Errorf("the BMC says it is %+v (%v)", device, err)
	}

	got, err := conn.Sensors(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []bmc.Sensor{
		{Name: "System Temp", Kind: bmc.Temperature, Value: 31, Readable: true},
		{Name: "SAS3 F1 Temp", Kind: bmc.Temperature, Value: 68, Readable: true},
		{Name: "FAN1", Kind: bmc.Fan, Value: 4500, Readable: true},
		{Name: "FAN2", Kind: bmc.Fan, Value: 0, Readable: true},
		{Name: "FAN8", Kind: bmc.Fan},
	}
	if !slices.Equal(got, want) {
		t.Errorf("the sensors read\n%+v\nwant\n%+v", got, want)
	}

	// only the sensors wanted are read
	temps, err := conn.Sensors(t.Context(), func(_ string, kind bmc.Kind) bool { return kind == bmc.Temperature })
	if err != nil || len(temps) != 2 || temps[1].Name != "SAS3 F1 Temp" {
		t.Errorf("the temperatures alone read %+v (%v)", temps, err)
	}

	fans := supermicro.New(conn, bmc.Options{})
	if d, err := fans.Duty(t.Context(), 0); err != nil || d != 60 {
		t.Fatalf("zone 0 reads %d (%v), want 60", d, err)
	}
	if err := fans.SetDuty(t.Context(), 0, 45); err != nil {
		t.Fatal(err)
	}
	if d, err := fans.Duty(t.Context(), 0); err != nil || d != 45 {
		t.Errorf("zone 0 reads %d (%v) after being set to 45", d, err)
	}

	// the BMC's word that it will not is told apart from a BMC that does not answer
	_, err = conn.Raw(t.Context(), 0x30, 0x99)
	var refused *bmc.CompletionError
	if !errors.As(err, &refused) || refused.Code != 0xc1 {
		t.Errorf("a command the BMC does not know gave %v", err)
	}
}

// A wrong password is said at once, naming the BMC.
func TestAWrongPassword(t *testing.T) {
	t.Parallel()

	host, port := bmctest.Shelf(60).Serve(t, "127.0.0.1:0", "hunter2")
	_, err := bmc.Dial(t.Context(), bmc.Address{Host: host, Port: port, User: bmctest.User, Password: "wrong", Timeout: 500 * time.Millisecond})
	if err == nil {
		t.Fatal("logged in with the wrong password")
	}
	t.Log(err)
}
