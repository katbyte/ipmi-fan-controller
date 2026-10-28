package sensors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// smartLimit is how long smartctl is given for one disk, which a disk that has stopped answering would otherwise hold up every other disk for.
const smartLimit = 10 * time.Second

// run runs a program and gives back what it printed to its output. It is stopped when the context ends.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // G204: the program is the smartctl the settings name, given a disk's name
}

// Smartctl is the smartctl the setting names: none for off, the one on the PATH for auto when there is one, and otherwise the path it gives.
func Smartctl(setting string) string {
	switch setting {
	case "off":
		return ""
	case "", "auto":
		path, err := exec.LookPath("smartctl")
		if err != nil {
			return ""
		}

		return path
	default:
		return setting
	}
}

// smartAnswer is the part of what smartctl --json prints that is read here.
type smartAnswer struct {
	Temperature *struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	Smartctl struct {
		Messages []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
}

// smart asks smartctl for the temperature of each disk wanted that the kernel lists and is not in seen. A disk that is spun down is left to sleep,
// and is not counted as unread: it is not warming anything.
func (m *Machine) smart(ctx context.Context, seen map[string]bool, want Want, out *Read) {
	entries, err := os.ReadDir(m.sys("block"))
	if err != nil {
		return
	}

	for _, e := range entries {
		name := e.Name()
		if seen[name] || !strings.HasPrefix(name, "sd") {
			continue
		}
		if ctx.Err() != nil {
			return
		}

		d := Disk{Name: name, Via: "smartctl"}
		if device, err := filepath.EvalSymlinks(m.sys("block", name, "device")); err == nil {
			describe(device, &d)
			d.Name = name
		}
		if !want.wants(d.id()) {
			continue
		}

		celsius, asleep, err := m.ask(ctx, name)
		switch {
		case asleep:
		case err != nil:
			out.unread(d.id(), err)
		default:
			out.Readings = append(out.Readings, Reading{ID: d.id(), Celsius: celsius, Disk: &d})
		}
	}
}

// ask asks smartctl for one disk's temperature, and says when the disk is asleep and was left so.
func (m *Machine) ask(ctx context.Context, name string) (celsius float64, asleep bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, smartLimit)
	defer cancel()

	// --nocheck=standby leaves a spun-down disk asleep, and smartctl then says so with exit status 2 and a message
	raw, runErr := m.run(ctx, m.smartctl, "--json", "--nocheck=standby", "--attributes", "/dev/"+name)

	var answer smartAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		if runErr != nil {
			return 0, false, fmt.Errorf("smartctl: %w", runErr)
		}

		return 0, false, fmt.Errorf("smartctl printed what is not its JSON: %w", err)
	}

	if answer.Temperature != nil && answer.Temperature.Current != nil {
		return *answer.Temperature.Current, false, nil
	}

	var said []string
	for _, msg := range answer.Smartctl.Messages {
		if strings.Contains(strings.ToUpper(msg.String), "STANDBY") || strings.Contains(strings.ToUpper(msg.String), "SLEEP") {
			return 0, true, nil
		}
		said = append(said, msg.String)
	}
	if len(said) > 0 {
		return 0, false, errors.New("smartctl: " + strings.Join(said, "; "))
	}

	return 0, false, errors.New("smartctl gave no temperature for it")
}
