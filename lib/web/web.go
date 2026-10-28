// Package web is the program's web page, which is off unless turned on: every zone, fan and temperature as the program last saw them, charted over
// the last hours, each zone's curves to change, holds to run a zone quiet or flat out for a while, and the settings file to edit whole. A change is
// checked before it is saved, saved into the settings file with its comments kept, and taken up at once.
//
// Anyone who can reach the page can see it. Changing anything needs the token, when one is set, and a request the browser only sends when the page
// itself asks, so another site cannot make a visitor's browser change the fans.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/ipmi-fan-controller/lib/config"
	"github.com/katbyte/ipmi-fan-controller/lib/control"
)

//go:embed page
var page embed.FS

// most is the largest request the page takes: a settings file is a few kilobytes.
const most = 1 << 20

// Controller is what the page reads and changes: control.Controller.
type Controller interface {
	State() control.State
	History(since int64) control.History
	Apply(cfg *config.Config)
	Hold(zone string, h control.Hold) error
	Release(zone string) error
	StartProbe(zone string, d time.Duration) error
	StopProbe(zone string) error
}

// Options is what the page is made with.
type Options struct {
	Controller Controller
	// Path is the settings file, read afresh for each change so one made to it by hand since is not lost.
	Path string
	// Parse checks a settings file's text and makes it what the controller runs on.
	Parse func(text []byte) (*config.Config, error)
	// Token, when set, is needed for any change.
	Token string
	// Name and Version are what the page says it is.
	Name, Version string
	// Now is the clock, time.Now unless a test says otherwise.
	Now func() time.Time
}

// server serves the page.
type server struct {
	o Options
	// mu makes two changes to the settings file take turns
	mu sync.Mutex
}

// Handler is the page and what it asks.
func Handler(o Options) http.Handler {
	if o.Now == nil {
		o.Now = time.Now
	}

	s := &server{o: o}
	files, _ := fs.Sub(page, "page")

	// the page's own files are asked for afresh each time, so a new version of the program never serves an old page from the browser's cache
	static := http.FileServerFS(files)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/about", s.about)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("PUT /api/config", s.change(s.putConfig))
	mux.HandleFunc("PUT /api/zones/{zone}", s.change(s.putZone))
	mux.HandleFunc("POST /api/zones/{zone}/hold", s.change(s.hold))
	mux.HandleFunc("DELETE /api/zones/{zone}/hold", s.change(s.release))
	mux.HandleFunc("POST /api/zones/{zone}/probe", s.change(s.startProbe))
	mux.HandleFunc("DELETE /api/zones/{zone}/probe", s.change(s.stopProbe))

	return mux
}

// answer writes v as JSON.
func answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// problems is what a refused change answers with: each thing wrong with it.
type problems struct {
	Errors []string `json:"errors"`
}

func refuse(w http.ResponseWriter, status int, err error) {
	var each []string
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, e := range joined.Unwrap() {
			each = append(each, e.Error())
		}
	} else {
		each = strings.Split(err.Error(), "\n")
	}

	answer(w, status, problems{Errors: each})
}

func (s *server) about(w http.ResponseWriter, _ *http.Request) {
	answer(w, http.StatusOK, map[string]any{"name": s.o.Name, "version": s.o.Version, "token": s.o.Token != ""})
}

func (s *server) state(w http.ResponseWriter, _ *http.Request) {
	answer(w, http.StatusOK, s.o.Controller.State())
}

// history is what the charts show: the rounds after ?since=, every one kept without it.
func (s *server) history(w http.ResponseWriter, r *http.Request) {
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			refuse(w, http.StatusBadRequest, fmt.Errorf("since is a round's number, not %q", v))
			return
		}
		since = n
	}

	answer(w, http.StatusOK, s.o.Controller.History(since))
}

// configAnswer is the settings file as the page shows it.
type configAnswer struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Text   string `json:"text"`
}

func (s *server) config(w http.ResponseWriter, _ *http.Request) {
	f, err := config.Read(s.o.Path)
	if err != nil {
		refuse(w, http.StatusInternalServerError, err)
		return
	}

	answer(w, http.StatusOK, configAnswer{Path: f.Path, Exists: f.Exists, Text: string(f.Text)})
}

// change guards a handler that changes something: it needs the token when one is set, and JSON, which a browser will not send to another site
// without that site's leave.
func (s *server) change(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.o.Token != "" {
			given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(s.o.Token)) != 1 {
				refuse(w, http.StatusUnauthorized, errors.New("changing anything needs the token"))
				return
			}
		}

		if r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			refuse(w, http.StatusUnsupportedMediaType, errors.New("a change is sent as JSON"))
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, most)
		next(w, r)
	}
}

// read reads a change's JSON.
func read(r *http.Request, v any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("the change is not the JSON expected: %w", err)
	}

	return nil
}

// save checks the settings' new text, saves it and has the controller take it up. Settings with anything wrong in them are neither saved nor taken up.
func (s *server) save(f *config.File, text []byte) error {
	cfg, err := s.o.Parse(text)
	if err != nil {
		return err
	}

	if err := f.Save(text); err != nil {
		return err
	}

	s.o.Controller.Apply(cfg)
	clog.Log.Infof("the web page saved %s, and it was taken up", f.Path)

	return nil
}

func (s *server) putConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := read(r, &body); err != nil {
		refuse(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := config.Read(s.o.Path)
	if err != nil {
		refuse(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.save(f, []byte(body.Text)); err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}

	answer(w, http.StatusOK, configAnswer{Path: f.Path, Exists: true, Text: string(f.Text)})
}

func (s *server) putZone(w http.ResponseWriter, r *http.Request) {
	var edit config.ZoneEdit
	if err := read(r, &edit); err != nil {
		refuse(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := config.Read(s.o.Path)
	if err != nil {
		refuse(w, http.StatusInternalServerError, err)
		return
	}

	text, err := config.EditZone(f.Text, r.PathValue("zone"), edit)
	if err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.save(f, text); err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}

	answer(w, http.StatusOK, configAnswer{Path: f.Path, Exists: true, Text: string(f.Text)})
}

// holdRequest is a hold from the page: full speed, or quiet at a duty, for so many minutes, or until let go for none.
type holdRequest struct {
	Full    bool `json:"full"`
	Duty    int  `json:"duty"`
	Minutes int  `json:"minutes"`
}

func (s *server) hold(w http.ResponseWriter, r *http.Request) {
	var body holdRequest
	if err := read(r, &body); err != nil {
		refuse(w, http.StatusBadRequest, err)
		return
	}
	if body.Minutes < 0 {
		refuse(w, http.StatusBadRequest, fmt.Errorf("%d minutes is not a while", body.Minutes))
		return
	}

	h := control.Hold{Full: body.Full, Duty: body.Duty}
	if body.Minutes > 0 {
		h.Until = s.o.Now().Add(time.Duration(body.Minutes) * time.Minute)
	}

	if err := s.o.Controller.Hold(r.PathValue("zone"), h); err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}

	answer(w, http.StatusOK, h)
}

func (s *server) release(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Controller.Release(r.PathValue("zone")); err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}

	answer(w, http.StatusOK, struct{}{})
}

// startProbe runs a zone flat out for {"minutes": 15} to see which disks it cools.
func (s *server) startProbe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes int `json:"minutes"`
	}
	if err := read(r, &body); err != nil {
		refuse(w, http.StatusBadRequest, err)
		return
	}

	if err := s.o.Controller.StartProbe(r.PathValue("zone"), time.Duration(body.Minutes)*time.Minute); err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}

	answer(w, http.StatusOK, struct{}{})
}

// stopProbe ends a zone's test early, or puts away one that is over.
func (s *server) stopProbe(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Controller.StopProbe(r.PathValue("zone")); err != nil {
		refuse(w, http.StatusUnprocessableEntity, err)
		return
	}

	answer(w, http.StatusOK, struct{}{})
}
