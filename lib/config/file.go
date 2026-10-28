package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// typeOfConfig is what the file's top level is read into, for finding the settings it has that nothing reads.
var typeOfConfig = reflect.TypeFor[Config]()

// unknown finds the settings in the file that no part of the program reads, a name mistyped say, which would otherwise be quietly left at its
// default: floor 30 for a "flor: 20" is the kind of mistake that is only found once the disks are hot.
func unknown(n *yaml.Node, t reflect.Type, where string) []error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch {
	case n.Kind == yaml.DocumentNode && len(n.Content) > 0:
		return unknown(n.Content[0], t, where)

	case n.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice:
		var problems []error
		for i, item := range n.Content {
			problems = append(problems, unknown(item, t.Elem(), where+"["+strconv.Itoa(i)+"]")...)
		}

		return problems

	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Struct:
		fields := map[string]reflect.Type{}
		var names []string
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "" || name == "-" {
				continue
			}
			fields[name] = f.Type
			names = append(names, name)
		}

		var problems []error
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			at := strings.TrimPrefix(where+"."+key, ".")
			ft, ok := fields[key]
			if !ok {
				problems = append(problems, fmt.Errorf("line %d: %s is not a setting: %s has %s", n.Content[i].Line, at, cmpOr(where, "the file"), strings.Join(names, ", ")))
				continue
			}

			problems = append(problems, unknown(n.Content[i+1], ft, at)...)
		}

		return problems
	}

	return nil
}

// File is a settings file and what it holds now: its text, so a change made on the web page is saved into it with its comments and layout kept.
type File struct {
	// Path is where it is, or would be written when it is not there yet.
	Path string
	// Exists says it was there to read.
	Exists bool
	// Text is what it holds.
	Text []byte
}

// Read reads the settings file at path. One that is not there is an empty file, to be written there when a change is saved.
func Read(path string) (*File, error) {
	text, err := os.ReadFile(path) //nolint:gosec // G304: the file is the one the settings name
	if errors.Is(err, fs.ErrNotExist) {
		return &File{Path: path}, nil
	}
	if err != nil {
		return nil, err
	}

	return &File{Path: path, Exists: true, Text: text}, nil
}

// Save writes text in place of the file's, whole or not at all where the file's folder lets it be: written beside it and moved over it. A file
// mounted into a container on its own cannot be moved over, so it is then written over in place.
func (f *File) Save(text []byte) error {
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(f.Path); err == nil {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.Path)+".*")
	if err == nil {
		_, err = tmp.Write(text)
		err = errors.Join(err, tmp.Chmod(mode), tmp.Close())
		if err == nil {
			err = os.Rename(tmp.Name(), f.Path)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}

	if err != nil {
		if werr := os.WriteFile(f.Path, text, mode); werr != nil {
			return fmt.Errorf("writing %s: %w", f.Path, errors.Join(err, werr))
		}
	}

	f.Exists, f.Text = true, text

	return nil
}

// ZoneEdit is a change to one zone made on the web page: each setting given is set, each curve given replaces the follow's of that number, Drop
// takes away the follows of those numbers, and Add adds follows with curves of their own. The numbers are of the follows as they were before the
// change. Follow adds a disk pattern, disk/<enclosure>/* say, to what the zone follows.
type ZoneEdit struct {
	Floor    *int          `json:"floor,omitempty"`
	Ceiling  *int          `json:"ceiling,omitempty"`
	Failsafe *int          `json:"failsafe,omitempty"`
	Curves   map[int]Curve `json:"curves,omitempty"`
	Drop     []int         `json:"drop,omitempty"`
	Add      []NewFollow   `json:"add,omitempty"`
	Follow   string        `json:"follow,omitempty"`
}

// NewFollow is a follow added on the web page: what it is called, what it follows, and its curve.
type NewFollow struct {
	Name    string   `json:"name"`
	Sensors []string `json:"sensors"`
	Curve   Curve    `json:"curve"`
}

// node is the follow as it is written in the file, its sensors and curve each on one line.
func (f NewFollow) node() *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if f.Name != "" {
		set(n, "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: strTag, Value: f.Name})
	}

	sensors := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, s := range f.Sensors {
		sensors.Content = append(sensors.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: strTag, Value: s})
	}
	set(n, "sensors", sensors)
	set(n, "curve", f.Curve.Node())

	return n
}

// DiskCurve is the curve a zone's first disks are followed by when they are added on the web page: quiet while they are cool, flat out before a
// disk reaches the 60° most are rated for. It is meant to be changed.
var DiskCurve = Curve{{Celsius: 40, Duty: 30}, {Celsius: 50, Duty: 45}, {Celsius: 58, Duty: 100}}

// EditZone is the file's text with one zone changed, and everything else in it as it was, comments too.
func EditZone(text []byte, zone string, edit ZoneEdit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("the settings file has no zones, and so no %s", zone)
	}

	z := zoneNode(doc.Content[0], zone)
	if z == nil {
		return nil, fmt.Errorf("the settings file has no zone called %s", zone)
	}

	settings := []struct {
		key   string
		value *int
	}{{"floor", edit.Floor}, {"ceiling", edit.Ceiling}, {"failsafe", edit.Failsafe}}
	for _, s := range settings {
		if s.value != nil {
			set(z, s.key, &yaml.Node{Kind: yaml.ScalarNode, Tag: intTag, Value: strconv.Itoa(*s.value)})
		}
	}

	if len(edit.Curves) > 0 || len(edit.Drop) > 0 {
		follows := get(z, "follow")
		if follows == nil || follows.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("zone %s follows nothing, so has no curve to change", zone)
		}
		for i, curve := range edit.Curves {
			if i < 0 || i >= len(follows.Content) {
				return nil, fmt.Errorf("zone %s has no follow %d", zone, i)
			}
			set(follows.Content[i], "curve", curve.Node())
		}

		drop := slices.Compact(slices.Sorted(slices.Values(edit.Drop)))
		for _, i := range slices.Backward(drop) {
			if i < 0 || i >= len(follows.Content) {
				return nil, fmt.Errorf("zone %s has no follow %d", zone, i)
			}
			follows.Content = slices.Delete(follows.Content, i, i+1)
		}
	}

	if len(edit.Add) > 0 {
		follows := followsOf(z)
		for _, f := range edit.Add {
			follows.Content = append(follows.Content, f.node())
		}
	}

	if edit.Follow != "" {
		if err := addFollow(z, zone, edit.Follow); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// addFollow adds a disk pattern to the zone's follow of disks alone, so it is followed with the disks already there, by the same curve; a zone with
// none gets a new one, by DiskCurve.
func addFollow(z *yaml.Node, zone, pattern string) error {
	if !strings.HasPrefix(pattern, "disk/") {
		return fmt.Errorf("only disks can be added to a zone's follows this way, not %s", pattern)
	}

	follows := followsOf(z)
	for _, f := range follows.Content {
		sensors := get(f, "sensors")
		if sensors == nil || sensors.Kind != yaml.SequenceNode || len(sensors.Content) == 0 {
			continue
		}
		if !slices.ContainsFunc(sensors.Content, func(n *yaml.Node) bool { return !strings.HasPrefix(n.Value, "disk/") }) {
			if slices.ContainsFunc(sensors.Content, func(n *yaml.Node) bool { return n.Value == pattern }) {
				return fmt.Errorf("zone %s already follows %s", zone, pattern)
			}
			sensors.Content = append(sensors.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: strTag, Value: pattern})

			return nil
		}
	}

	follows.Content = append(follows.Content, NewFollow{Name: "disks", Sensors: []string{pattern}, Curve: DiskCurve}.node())

	return nil
}

// followsOf is the zone's list of follows, made when it has none.
func followsOf(z *yaml.Node) *yaml.Node {
	follows := get(z, "follow")
	if follows == nil || follows.Kind != yaml.SequenceNode {
		follows = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		set(z, "follow", follows)
	}

	return follows
}

// zoneNode is the zone of that name in the file's top level, or nil.
func zoneNode(top *yaml.Node, name string) *yaml.Node {
	zones := get(top, "zones")
	if zones == nil || zones.Kind != yaml.SequenceNode {
		return nil
	}

	for _, z := range zones.Content {
		if n := get(z, "name"); n != nil && n.Value == name {
			return z
		}
	}

	return nil
}

// get is the value of a key in a mapping, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}

	return nil
}

// set sets a key in a mapping, keeping the comments on the value it replaces, or adds the key at the end.
func set(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			value.LineComment, value.HeadComment, value.FootComment = old.LineComment, old.HeadComment, old.FootComment
			m.Content[i+1] = value

			return
		}
	}

	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: strTag, Value: key}, value)
}
