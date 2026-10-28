//go:build acceptance

package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/bmctest"
)

// image is the program's own image, as make docker builds it. make testacc builds it and names it here.
var image = os.Getenv("IPMI_FAN_CONTROLLER_TEST_PROGRAM_IMAGE")

// patience is how long a page or a change is waited for before a test gives up on it.
const patience = 30 * time.Second

// password is the made-up shelf's.
const password = "hunter2"

// docker runs one docker command and gives back what it printed. It is not stopped when the test ends, so a cleanup can still put things back.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), "docker", args...) //nolint:gosec // every argument is one a test here wrote
	out, err := cmd.CombinedOutput()

	return strings.TrimSpace(string(out)), err
}

// shelf is a made-up disk shelf's BMC, found with its fans at 60%, served on every address of this machine so a container can reach it, and the
// settings that have its zone 0 follow its SAS chip's temperature.
func shelf(t *testing.T) (board *bmctest.Board, settings string) {
	t.Helper()

	board = bmctest.Shelf(60).Sensor("SAS3 F1 Temp", bmc.Temperature, 68).Sensor("FAN1", bmc.Fan, 4500).Unreadable("FAN8", bmc.Fan)
	_, port := board.Serve(t, "0.0.0.0:0", password)

	return board, `# a made-up shelf, served by the test
interval: 1s
bmcs:
  - name: annex
    host: host.docker.internal
    port: ` + strconv.Itoa(port) + `
    user: ` + bmctest.User + `
    password: ${ANNEX_PASSWORD}
zones:
  - name: shelf
    bmc: annex
    zone: 0
    follow:
      - name: sas chip
        sensors: [ipmi/annex/SAS3 F1 Temp]
        curve: {60: 30, 80: 100}
`
}

// created makes a container of the image under that name with the settings in /config, and removes it when the test ends. It is not started. With
// the web page on, its port is published.
func created(t *testing.T, name, settings string, web bool) {
	t.Helper()

	if image == "" {
		t.Skip("no image to run: make testacc builds one and names it in IPMI_FAN_CONTROLLER_TEST_PROGRAM_IMAGE")
	}

	_, _ = docker(t, "rm", "--force", name)
	t.Cleanup(func() { _, _ = docker(t, "rm", "--force", name) })

	// host.docker.internal is the machine docker runs on, where the test serves the shelf: docker desktop names it itself, and linux is told to.
	// The health check is run every two seconds where the image says thirty, so the test need not wait half a minute for the first
	args := make([]string, 0, 16)
	args = append(args, "create", "--name", name, "--add-host", "host.docker.internal:host-gateway", "--health-interval", "2s", "--env", "ANNEX_PASSWORD="+password, "--env", "IPMI_FAN_CONTROLLER_LOG=INFO")
	if web {
		args = append(args, "--publish", "127.0.0.1::9126", "--env", "IPMI_FAN_CONTROLLER_WEB=true")
	}
	if out, err := docker(t, append(args, image)...); err != nil {
		t.Fatalf("making a container of the image %s: %v: %s", image, err, out)
	}

	// anyone may write it, as the web page writes it back as the image's own user, which is not the one copying it in
	file := filepath.Join(t.TempDir(), "ipmi-fan-controller.yaml")
	if err := os.WriteFile(file, []byte(settings), 0o666); err != nil { //nolint:gosec // G306: see above
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o666); err != nil { //nolint:gosec // G302: see above
		t.Fatal(err)
	}
	if out, err := docker(t, "cp", file, name+":/config/ipmi-fan-controller.yaml"); err != nil {
		t.Fatalf("handing the container its settings: %v: %s", err, out)
	}
}

// started starts a container made by created.
func started(t *testing.T, name string) {
	t.Helper()

	if out, err := docker(t, "start", name); err != nil {
		t.Fatalf("starting the container: %v: %s", err, out)
	}
}

// published is where a started container's web page is.
func published(t *testing.T, name string) string {
	t.Helper()

	at, err := docker(t, "port", name, "9126/tcp")
	if err != nil || at == "" {
		logs, _ := docker(t, "logs", name)
		t.Fatalf("finding where the image's port is published: %v: %s\nits log:\n%s", err, at, logs)
	}

	return "http://" + strings.TrimSpace(strings.Split(at, "\n")[0])
}

// get fetches a page, and gives back what it said, or "" when it could not be fetched.
func get(t *testing.T, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = res.Body.Close() }()

	raw, _ := io.ReadAll(res.Body)

	return string(raw)
}

// until waits for check to pass, and says whether it did.
func until(check func() bool) bool {
	deadline := time.Now().Add(patience)
	for !check() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}

	return true
}

// The image a deployment runs, given settings for a shelf on the network and no web page: it opens no port, logs in to the shelf from inside the
// container, sets its zone to what the curve asks for at its chip's temperature, is healthy by its own check, runs as someone other than root,
// and when docker stops it puts the shelf back at the 60% it found it at.
func TestTheImageSetsAShelf(t *testing.T) {
	board, settings := shelf(t)
	name := "ipmi-fan-controller-acceptance-shelf"
	created(t, name, settings, false)
	started(t, name)

	// 68° on a curve from 30% at 60° to 100% at 80° is 58%
	if !until(func() bool { return board.Duty(0) == 58 }) {
		logs, _ := docker(t, "logs", name)
		t.Fatalf("the shelf is at %d%%, never set to 58%%; the image's log:\n%s", board.Duty(0), logs)
	}

	if ports, err := docker(t, "port", name); err != nil || ports != "" {
		t.Errorf("the image publishes %q (%v), want nothing", ports, err)
	}
	if who, err := docker(t, "exec", name, "id", "-u"); err != nil || who == "" || who == "0" {
		t.Errorf("the program runs as user %q (%v), want someone other than root", who, err)
	}
	if !until(func() bool {
		health, _ := docker(t, "inspect", "--format", "{{.State.Health.Status}}", name)
		return health == "healthy"
	}) {
		health, _ := docker(t, "inspect", "--format", "{{json .State.Health}}", name)
		t.Errorf("the image's own health check never passed: %s", health)
	}

	if out, err := docker(t, "stop", "--time", "30", name); err != nil {
		t.Fatalf("stopping the container: %v: %s", err, out)
	}
	if board.Duty(0) != 60 {
		logs, _ := docker(t, "logs", name)
		t.Errorf("stopped, the image left the shelf at %d%%, want the 60%% it was found at; its log:\n%s", board.Duty(0), logs)
	}
	if got := board.Writes(); len(got) != 2 || got[0] != "zone 0 58" || got[1] != "zone 0 60" {
		t.Errorf("the image sent %v", got)
	}
}

// With the web page on, the image serves it on its port, showing the shelf as it set it, and reads the shelf's fans for it to show.
func TestTheImagesWebPage(t *testing.T) {
	board, settings := shelf(t)
	name := "ipmi-fan-controller-acceptance-web"
	created(t, name, settings, true)
	started(t, name)
	base := published(t, name)

	var st struct {
		Zones []struct {
			Duty *int `json:"duty"`
		} `json:"zones"`
		BMCs []struct {
			Fans []struct {
				Name string  `json:"name"`
				RPM  float64 `json:"rpm"`
			} `json:"fans"`
		} `json:"bmcs"`
	}
	shown := func() bool {
		return json.Unmarshal([]byte(get(t, base+"/api/state")), &st) == nil && len(st.Zones) == 1 && st.Zones[0].Duty != nil && *st.Zones[0].Duty == 58
	}
	if !until(shown) {
		logs, _ := docker(t, "logs", name)
		t.Fatalf("the page never showed the shelf at 58%% (it is at %d%%); the image's log:\n%s", board.Duty(0), logs)
	}
	if len(st.BMCs) != 1 || len(st.BMCs[0].Fans) != 2 || st.BMCs[0].Fans[0].RPM != 4500 {
		t.Errorf("the page shows the fans as %+v", st.BMCs)
	}
	if body := get(t, base+"/"); !strings.Contains(body, "<title>ipmi-fan-controller</title>") {
		t.Errorf("the web page is %.200s", body)
	}
}

// Settings with a mistake in them stop the image at once, naming the mistake, and the shelf is never touched.
func TestTheImageRefusesBadSettings(t *testing.T) {
	board, settings := shelf(t)
	name := "ipmi-fan-controller-acceptance-bad"
	created(t, name, strings.Replace(settings, "    zone: 0\n", "    zone: 0\n    flor: 10\n", 1), false)

	out, err := docker(t, "start", "--attach", name)
	if err == nil || !strings.Contains(out, "zones[0].flor is not a setting") {
		t.Errorf("the image with a mistake in its settings gave %v:\n%s", err, out)
	}
	if len(board.Writes()) != 0 {
		t.Errorf("the shelf was set: %v", board.Writes())
	}
}
