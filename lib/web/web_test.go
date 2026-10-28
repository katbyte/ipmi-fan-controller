package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/ipmi-fan-controller/lib/bmc"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/bmctest"
	"github.com/katbyte/ipmi-fan-controller/lib/bmc/makers"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/control"
	"github.com/katbyte/ipmi-fan-controller/lib/sensors"
	"github.com/katbyte/ipmi-fan-controller/lib/web"
)

// machine is a made-up machine with one disk in lab's shelf, at 50°.
type machine struct{}

func (machine) Chips(context.Context, sensors.Want) (sensors.Read, error) { return sensors.Read{}, nil }

func (machine) Disks(context.Context, sensors.Want) (sensors.Read, error) {
	return sensors.Read{Readings: []sensors.Reading{{ID: "disk/5003048000f8a93f/ZR10A001", Celsius: 50}}}, nil
}

const settings = `# lab's shelf
bmcs:
  - name: annex
    host: 192.0.2.10
    user: ADMIN
    password: x
zones:
  - name: shelf
    bmc: annex
    zone: 0
    follow:
      - sensors: [disk/5003048000f8a93f/*]
        curve: {35: 30, 45: 50, 55: 100} # quiet until 35°
`

// site is the page on a controller of lab's shelf, with its settings file, and the token it needs when one is given.
type site struct {
	*httptest.Server

	ctrl  *control.Controller
	shelf *bmctest.Board
	path  string
	token string
}

func newSite(t *testing.T, token string) *site {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ipmi-fan-controller.yaml")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	parse := func(text []byte) (*config.Config, error) { return config.Parse(text, makers.Names()) }
	cfg, err := parse([]byte(settings))
	if err != nil {
		t.Fatal(err)
	}

	shelf := bmctest.Shelf(60)
	boards := bmctest.Boards{"annex": shelf}
	noon := func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	ctrl := control.New(control.Options{Config: cfg, Machine: machine{}, Dial: func(ctx context.Context, b config.BMC) (bmc.Conn, error) { return boards.Dial(ctx, b.Name) }, ReadAll: true, Now: noon})
	ctrl.Round(t.Context())

	srv := httptest.NewServer(web.Handler(web.Options{Controller: ctrl, Path: path, Parse: parse, Token: token, Name: "ipmi-fan-controller", Version: "test", Now: noon}))
	t.Cleanup(srv.Close)

	return &site{Server: srv, ctrl: ctrl, shelf: shelf, path: path, token: token}
}

// ask sends a request as the page does, and gives back the status and the answer.
func (s *site) ask(t *testing.T, method, path, contentType, body string) (status int, answer string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(raw)
}

// The page itself, the state and the settings file are there to read.
func TestReading(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")

	if status, body := s.ask(t, http.MethodGet, "/", "", ""); status != http.StatusOK || !strings.Contains(body, "<title>ipmi-fan-controller</title>") {
		t.Errorf("the page answered %d: %.200s", status, body)
	}

	status, body := s.ask(t, http.MethodGet, "/api/state", "", "")
	var st control.State
	if err := json.Unmarshal([]byte(body), &st); err != nil || status != http.StatusOK || len(st.Zones) != 1 || st.Zones[0].Want != 75 {
		t.Errorf("the state answered %d (%v): %.300s", status, err, body)
	}

	status, body = s.ask(t, http.MethodGet, "/api/config", "", "")
	if status != http.StatusOK || !strings.Contains(body, "# lab's shelf") || !strings.Contains(body, s.path) {
		t.Errorf("the settings answered %d: %s", status, body)
	}

	status, body = s.ask(t, http.MethodGet, "/api/history", "", "")
	var h control.History
	if err := json.Unmarshal([]byte(body), &h); err != nil || status != http.StatusOK || len(h.Rounds) != 1 || len(h.Series["duty/shelf"]) != 1 {
		t.Errorf("the history answered %d (%v): %.300s", status, err, body)
	}
	if status, body = s.ask(t, http.MethodGet, "/api/history?since=1", "", ""); status != http.StatusOK || !strings.Contains(body, `"rounds":[]`) {
		t.Errorf("the history since round 1 answered %d: %.300s", status, body)
	}
	if status, body = s.ask(t, http.MethodGet, "/api/history?since=soon", "", ""); status != http.StatusBadRequest {
		t.Errorf("the history since soon answered %d: %.300s", status, body)
	}
}

// A curve changed on the page is saved into the settings file with its comments, and taken up at once.
func TestChangingACurve(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	status, body := s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"floor": 35, "curves": {"0": [{"celsius": 40, "duty": 35}, {"celsius": 60, "duty": 100}]}}`)
	if status != http.StatusOK {
		t.Fatalf("the change answered %d: %s", status, body)
	}

	saved, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# lab's shelf", "curve: {40: 35, 60: 100} # quiet until 35°", "floor: 35"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("the saved file lacks %q:\n%s", want, saved)
		}
	}

	s.ctrl.Round(t.Context())
	if z := s.ctrl.State().Zones[0]; z.Want != 68 || z.Floor != 35 || s.shelf.Duty(0) != 68 {
		t.Errorf("after the change the zone is %+v at %d%%, want 68%% from the new curve at 50°", z, s.shelf.Duty(0))
	}
}

// A curve of its own added on the page is saved with what it follows, and one taken away is gone.
func TestACurveOfItsOwn(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	status, body := s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"add": [{"name": "inside", "sensors": ["ipmi/annex/System Temp"], "curve": [{"celsius": 30, "duty": 30}, {"celsius": 45, "duty": 80}]}]}`)
	if status != http.StatusOK {
		t.Fatalf("adding a curve answered %d: %s", status, body)
	}
	saved, _ := os.ReadFile(s.path)
	if !strings.Contains(string(saved), "name: inside") || !strings.Contains(string(saved), "sensors: [ipmi/annex/System Temp]") || !strings.Contains(string(saved), "curve: {30: 30, 45: 80}") {
		t.Errorf("the saved file lacks the new curve:\n%s", saved)
	}

	if status, body = s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"drop": [1]}`); status != http.StatusOK {
		t.Fatalf("taking the curve away answered %d: %s", status, body)
	}
	saved, _ = os.ReadFile(s.path)
	if strings.Contains(string(saved), "inside") || !strings.Contains(string(saved), "# quiet until 35°") {
		t.Errorf("the curve taken away is still there, or the one kept lost its comment:\n%s", saved)
	}
}

// Settings with a mistake in them are refused, with every mistake named, and neither saved nor taken up.
func TestABadChange(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	status, body := s.ask(t, http.MethodPut, "/api/config", "application/json", `{"text": "zones:\n  - name: shelf\n    bmc: nowhere\n    flor: 10\n"}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "flor is not a setting") {
		t.Errorf("the bad settings answered %d: %s", status, body)
	}

	status, body = s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"floor": 90, "ceiling": 50}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "floor (90) and ceiling (50)") {
		t.Errorf("the bad zone answered %d: %s", status, body)
	}

	status, body = s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"drop": [0]}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "a zone has to follow something") {
		t.Errorf("taking away the zone's only curve answered %d: %s", status, body)
	}

	saved, _ := os.ReadFile(s.path)
	if string(saved) != settings {
		t.Errorf("the settings file changed:\n%s", saved)
	}
}

// With a token set, nothing is changed without it, and everything can still be read.
func TestTheToken(t *testing.T) {
	t.Parallel()

	s := newSite(t, "sesame")
	s.token = ""
	if status, _ := s.ask(t, http.MethodPost, "/api/zones/shelf/hold", "application/json", `{"full": true}`); status != http.StatusUnauthorized {
		t.Errorf("a hold without the token answered %d", status)
	}
	if status, _ := s.ask(t, http.MethodGet, "/api/state", "", ""); status != http.StatusOK {
		t.Errorf("reading without the token answered %d", status)
	}

	s.token = "sesame"
	if status, body := s.ask(t, http.MethodPost, "/api/zones/shelf/hold", "application/json", `{"full": true}`); status != http.StatusOK {
		t.Errorf("a hold with the token answered %d: %s", status, body)
	}
}

// A change has to be JSON, which a browser will not send to another site without its leave, so another site cannot change the fans through a
// visitor's browser.
func TestAChangeFromAnotherSite(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	if status, _ := s.ask(t, http.MethodPost, "/api/zones/shelf/hold", "application/x-www-form-urlencoded", "full=true"); status != http.StatusUnsupportedMediaType {
		t.Errorf("a form answered %d", status)
	}
}

// A hold from the page runs the zone at once, for as long as it says, and letting go of it ends it.
func TestHolding(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	if status, body := s.ask(t, http.MethodPost, "/api/zones/shelf/hold", "application/json", `{"duty": 40, "minutes": 120}`); status != http.StatusOK || !strings.Contains(body, "2026-10-20T14:00:00Z") {
		t.Fatalf("the hold answered %d: %s", status, body)
	}

	s.ctrl.Round(t.Context())
	if z := s.ctrl.State().Zones[0]; z.State != control.Quiet || s.shelf.Duty(0) != 40 {
		t.Errorf("held, the zone is %s at %d%%", z.State, s.shelf.Duty(0))
	}

	if status, body := s.ask(t, http.MethodDelete, "/api/zones/shelf/hold", "", ""); status != http.StatusOK {
		t.Fatalf("letting go answered %d: %s", status, body)
	}
	s.ctrl.Round(t.Context())
	if z := s.ctrl.State().Zones[0]; z.State != control.Auto {
		t.Errorf("let go, the zone is %s", z.State)
	}

	if status, _ := s.ask(t, http.MethodPost, "/api/zones/attic/hold", "application/json", `{"full": true}`); status != http.StatusUnprocessableEntity {
		t.Errorf("holding a zone that is not there answered %d", status)
	}
}

// An enclosure no zone follows is added to a zone from the page, and saved into the settings file.
func TestFollowingAnEnclosure(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	status, body := s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"follow": "disk/5003048000a1b23f/*"}`)
	if status != http.StatusOK {
		t.Fatalf("adding the enclosure answered %d: %s", status, body)
	}

	raw, _ := os.ReadFile(s.path)
	if !strings.Contains(string(raw), "disk/5003048000a1b23f/*") {
		t.Errorf("the settings file is:\n%s", raw)
	}
	if z, _ := s.ctrl.Config().Zone("shelf"); len(z.Follow[0].Sensors) != 2 {
		t.Errorf("the shelf follows %+v", z.Follow)
	}

	if status, body = s.ask(t, http.MethodPut, "/api/zones/shelf", "application/json", `{"follow": "disk/5003048000a1b23f/*"}`); status != http.StatusUnprocessableEntity {
		t.Errorf("adding it again answered %d: %s", status, body)
	}
}

// A test of which disks a zone cools is started from the page, refused while one runs, and stopped.
func TestTestingAZone(t *testing.T) {
	t.Parallel()

	s := newSite(t, "")
	if status, body := s.ask(t, http.MethodPost, "/api/zones/shelf/probe", "application/json", `{"minutes": 15}`); status != http.StatusOK {
		t.Fatalf("starting the test answered %d: %s", status, body)
	}
	if status, body := s.ask(t, http.MethodPost, "/api/zones/shelf/probe", "application/json", `{"minutes": 15}`); status != http.StatusUnprocessableEntity || !strings.Contains(body, "one test at a time") {
		t.Errorf("a second test answered %d: %s", status, body)
	}
	if status, body := s.ask(t, http.MethodDelete, "/api/zones/shelf/probe", "", ""); status != http.StatusOK {
		t.Errorf("stopping the test answered %d: %s", status, body)
	}
	if status, body := s.ask(t, http.MethodPost, "/api/zones/shelf/probe", "application/json", `{"minutes": 600}`); status != http.StatusUnprocessableEntity {
		t.Errorf("a ten hour test answered %d: %s", status, body)
	}
}
