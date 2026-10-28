package bmc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bougou/go-ipmi/pkg/client"
	"github.com/bougou/go-ipmi/pkg/types"
)

// Address is where a BMC is and the login it takes. A blank Host is the machine's own BMC, asked through Linux's IPMI driver at /dev/ipmi0, which
// needs no login.
type Address struct {
	Host     string
	Port     int
	User     string
	Password string
	// Timeout is how long one command is given, and is given again three times over the network before the BMC is taken not to be answering.
	Timeout time.Duration
}

// Dial connects to a BMC: over the network with IPMI 2.0 (what ipmitool calls lanplus) when the address has a host, or through /dev/ipmi0 when it
// has none. No Go IPMI needs ipmitool or any C library.
func Dial(ctx context.Context, a Address) (Conn, error) {
	var c *client.Client
	var err error
	if a.Host == "" {
		c, err = client.NewOpenClient()
	} else {
		c, err = client.NewClient(a.Host, a.Port, a.User, a.Password)
	}
	if err != nil {
		return nil, err
	}

	if a.Timeout > 0 {
		c = c.WithTimeout(a.Timeout)
	}
	if a.Host != "" {
		c = c.WithRetry(3).WithMaxPrivilegeLevel(types.PrivilegeLevelAdministrator)
	}

	if err := c.Connect(ctx); err != nil {
		if a.Host == "" {
			return nil, fmt.Errorf("opening the machine's own BMC through /dev/ipmi0 (load Linux's ipmi_devintf and ipmi_si, and give a container the device): %w", err)
		}

		if strings.Contains(err.Error(), "authentication") {
			return nil, fmt.Errorf("logging in to %s: it does not take that user and password: %w", a.Host, err)
		}

		return nil, fmt.Errorf("logging in to %s: %w", a.Host, err)
	}

	return &ipmiConn{c: c}, nil
}

// ipmiConn is a BMC asked through go-ipmi.
type ipmiConn struct {
	c *client.Client

	mu sync.Mutex
	// sensors is the BMC's temperature and fan sensors, as its list of them says: read the first time they are asked for, and kept, since asking
	// for the list takes a few hundred commands and it only changes when the BMC's firmware does
	sensors []*types.Sensor
}

func (i *ipmiConn) Raw(ctx context.Context, netFn, cmd byte, data ...byte) ([]byte, error) {
	res, err := i.c.RawCommand(ctx, types.NetFn(netFn), cmd, data, fmt.Sprintf("raw %#02x %#02x", netFn, cmd))
	if err != nil {
		return nil, completion(err)
	}

	return res.Response, nil
}

// completion is go-ipmi's error with the BMC's completion code made a *CompletionError, so a maker can tell a command it does not know from a BMC
// that did not answer.
func completion(err error) error {
	if r, ok := types.IsResponseError(err); ok {
		return fmt.Errorf("%w (%s)", &CompletionError{Code: uint8(r.CompletionCode())}, err.Error())
	}

	return err
}

func (i *ipmiConn) Device(ctx context.Context) (Device, error) {
	res, err := i.c.GetDeviceID(ctx)
	if err != nil {
		return Device{}, completion(err)
	}

	return Device{Manufacturer: res.ManufacturerID, Product: res.ProductID, Firmware: strconv.Itoa(int(res.MajorFirmwareRevision)) + "." + fmt.Sprintf("%02x", res.MinorFirmwareRevision)}, nil
}

func (i *ipmiConn) Sensors(ctx context.Context, want Want) ([]Sensor, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.sensors == nil {
		listed, err := i.c.GetSensors(ctx, client.SensorFilterOptionIsSensorType(types.SensorTypeTemperature, types.SensorTypeFan))
		if err != nil {
			return nil, fmt.Errorf("reading the BMC's list of sensors: %w", completion(err))
		}
		i.sensors = listed
	}

	out := make([]Sensor, 0, len(i.sensors))
	for _, s := range i.sensors {
		if !want.Wants(strings.TrimSpace(s.Name), kindOf(s)) {
			continue
		}

		read, err := i.read(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", s.Name, err)
		}

		out = append(out, read)
	}

	return out, nil
}

// kindOf is what a sensor of the BMC's list measures: it lists only temperatures and fans.
func kindOf(s *types.Sensor) Kind {
	if s.SensorType == types.SensorTypeTemperature {
		return Temperature
	}

	return Fan
}

// read reads one sensor now. One the BMC has no reading for is not readable, which is not an error: a fan header with nothing plugged in.
func (i *ipmiConn) read(ctx context.Context, s *types.Sensor) (Sensor, error) {
	kind := kindOf(s)
	out := Sensor{Name: strings.TrimSpace(s.Name), Kind: kind}

	// a sensor is read from whichever controller owns it, which is the BMC itself for nearly all
	cc := &client.CommandContext{}
	cc.WithResponderAddr(s.GeneratorID.OwnerID()).WithResponderLUN(s.GeneratorID.LUN())
	ctx = client.WithCommandContext(ctx, cc)

	res, err := i.c.GetSensorReading(ctx, s.Number)
	if r, ok := types.IsResponseError(err); ok && r.CompletionCode() == types.CodeRequestedDataNotPresent {
		return out, nil
	}
	if err != nil {
		return out, completion(err)
	}
	if res.ReadingUnavailable || res.SensorScanningDisabled || !s.SensorUnit.IsAnalog() {
		return out, nil
	}

	reading := *s
	if s.Threshold.LinearizationFunc.IsNonLinear() {
		factors, err := i.c.GetSensorReadingFactors(ctx, s.Number, res.Reading)
		if err != nil {
			return out, completion(err)
		}
		reading.Threshold.ReadingFactors = factors.ReadingFactors
	}

	out.Value, out.Readable = reading.ConvertReading(res.Reading), true
	if kind == Temperature && s.SensorUnit.BaseUnit == types.SensorUnitType_DegreesF {
		out.Value = (out.Value - 32) * 5 / 9
	}

	return out, nil
}

func (i *ipmiConn) Close(ctx context.Context) error {
	err := i.c.Close(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}
