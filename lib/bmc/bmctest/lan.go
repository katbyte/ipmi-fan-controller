package bmctest

import (
	"context"
	"errors"
	"math"
	"net"
	"testing"

	gobmc "github.com/bougou/go-ipmi/pkg/bmc"
	"github.com/bougou/go-ipmi/pkg/clock"
	"github.com/bougou/go-ipmi/pkg/hal/mock"
	"github.com/bougou/go-ipmi/pkg/handlers"
	"github.com/bougou/go-ipmi/pkg/server"
	"github.com/bougou/go-ipmi/pkg/transport/udp"
	"github.com/bougou/go-ipmi/pkg/types"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
)

// User is the login a served board takes.
const User = "ADMIN"

// rpmPerCount is how many turns a minute each count of a fan's reading is, as a Supermicro board's sensor records say: a reading of 90 is 4500.
const rpmPerCount = 50

// Serve is the board as a BMC on the network, speaking IPMI 2.0 as a real one does, at addr ("127.0.0.1:0" for a port picked for it): what the
// program's own IPMI is tested against, wire and all. Its login is User with that password, and its sensors are the ones it has now, each in a
// full sensor record as a real board lists them. It stops when the test ends, and gives back where it is.
func (b *Board) Serve(tb testing.TB, addr, password string) (host string, port int) {
	tb.Helper()

	b.mu.Lock()
	device, sensors := b.device, append([]bmc.Sensor(nil), b.sensors...)
	b.mu.Unlock()

	hal := mock.New()
	for i, s := range sensors {
		if err := hal.Storage().SDR().Write(tb.Context(), uint16(i+1), record(uint16(i+1), uint8(i+1), s)); err != nil {
			tb.Fatalf("listing %s: %v", s.Name, err)
		}
	}

	info := gobmc.DeviceInfo{DeviceID: 32, FirmwareMajor: 1, FirmwareMinor: 0x33, IPMIVersion: 0x20, ManufacturerID: device.Manufacturer, ProductID: device.Product, AdditionalDeviceSupport: 0x3b}
	dev := gobmc.New(info, [16]byte{}, hal, gobmc.WithClock(clock.Real))

	user, err := dev.Users.Add(2, User)
	if err != nil {
		tb.Fatal(err)
	}
	user.SetPassword([]byte(password))
	user.Enabled = true
	user.ChannelAccess[1] = gobmc.UserChannelAccess{MaxPrivilege: gobmc.PrivilegeLevelAdministrator, Enabled: true}

	reg := handlers.NewRegistry()
	handlers.RegisterAllHandlers(reg)
	for _, cmd := range []uint8{0x45, 0x70} {
		reg.RegisterFunc(types.Command{ID: cmd, NetFn: 0x30, Name: "Supermicro fans"}, func(ctx context.Context, _ *handlers.HandlerContext, data []byte) ([]byte, types.CompletionCode, error) {
			out, rerr := b.Raw(ctx, 0x30, cmd, data...)

			return out, code(rerr), nil
		})
	}
	reg.RegisterFunc(types.Command{ID: 0x2d, NetFn: 0x04, Name: "Get Sensor Reading"}, func(_ context.Context, _ *handlers.HandlerContext, data []byte) ([]byte, types.CompletionCode, error) {
		if len(data) < 1 || int(data[0]) < 1 || int(data[0]) > len(sensors) {
			return nil, types.CodeRequestedDataNotPresent, nil
		}

		return b.reading(sensors[data[0]-1].Name), types.CodeOK, nil
	})

	conn, lerr := udp.Listen(addr)
	if lerr != nil {
		tb.Fatal(lerr)
	}

	srv := server.NewServer(dev, conn, server.WithHandlerRegistry(reg))
	ctx, cancel := context.WithCancel(context.WithoutCancel(tb.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	tb.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-done
	})

	at, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		tb.Fatalf("the BMC is at %v, which is not a UDP address", conn.LocalAddr())
	}

	return at.IP.String(), at.Port
}

// code is the completion code a board's error answers with.
func code(err error) types.CompletionCode {
	var c *bmc.CompletionError
	switch {
	case err == nil:
		return types.CodeOK
	case errors.As(err, &c):
		return types.CompletionCode(c.Code)
	default:
		return types.CodeUnspecifiedError
	}
}

// record is a sensor's full sensor record, which says what it measures and how to turn its reading's one byte into degrees or turns a minute.
func record(id uint16, number uint8, s bmc.Sensor) []byte {
	full := &types.SDRFull{
		GeneratorID:            0x0020,
		SensorNumber:           types.SensorNumber(number),
		SensorEntityID:         types.EntityID(0x07),
		SensorInitialization:   types.SensorInitialization{InitScanning: true, SensorScanningEnabled: true},
		SensorType:             types.SensorTypeTemperature,
		SensorEventReadingType: types.EventReadingTypeThreshold,
		SensorUnit:             types.SensorUnit{BaseUnit: types.SensorUnitType_DegreesC},
		M:                      1,
		IDStringTypeLength:     types.TypeLength(0xC0 | byte(len(s.Name))), //nolint:gosec // G115: a sensor's name is at most 16 bytes
		IDStringBytes:          []byte(s.Name),
	}
	if s.Kind == bmc.Fan {
		full.SensorType, full.SensorUnit.BaseUnit, full.M = types.SensorTypeFan, types.SensorUnitType_RPM, rpmPerCount
	}

	return full.Pack(id)
}

// reading is a sensor's reading as Get Sensor Reading answers it: the reading's byte, then that the sensor is scanned, with the bit that says it has
// no reading set for one that has none, then no thresholds crossed.
func (b *Board) reading(name string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, s := range b.sensors {
		if s.Name != name {
			continue
		}
		if !s.Readable {
			return []byte{0, 0x40 | 0x20, 0}
		}

		raw := s.Value
		if s.Kind == bmc.Fan {
			raw /= rpmPerCount
		}

		return []byte{byte(math.Round(max(0, min(raw, 255)))), 0x40, 0}
	}

	return []byte{0, 0x40 | 0x20, 0}
}
