// Package bmc talks to BMCs. What every BMC does the same way, reading its sensors and saying who made it, is here; setting its fans, which each
// maker does its own way, is behind Fans, with one package per maker.
package bmc

import (
	"context"
	"errors"
	"fmt"
)

// Conn is one BMC as the program talks to it.
type Conn interface {
	// Raw sends one command and gives back the answer's data. An answer that says the command failed is a *CompletionError.
	Raw(ctx context.Context, netFn, cmd byte, data ...byte) ([]byte, error)
	// Sensors reads the temperatures and fans the BMC has a sensor for that are wanted: every one for a nil want.
	Sensors(ctx context.Context, want Want) ([]Sensor, error)
	// Device is who made the BMC and what it runs.
	Device(ctx context.Context) (Device, error)
	Close(ctx context.Context) error
}

// Kind is what a sensor measures.
type Kind string

const (
	Temperature Kind = "temperature"
	Fan         Kind = "fan"
)

// Want says which of a BMC's sensors to read, by name and kind: nil for every one. One not wanted is not asked for its reading.
type Want func(name string, kind Kind) bool

// Wants says whether a sensor is wanted.
func (w Want) Wants(name string, kind Kind) bool {
	return w == nil || w(name, kind)
}

// Sensor is one of a BMC's sensors and what it reads now.
type Sensor struct {
	// Name is the BMC's own name for it: CPU Temp, FAN1.
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Value is in degrees for a temperature and turns a minute for a fan.
	Value float64 `json:"value"`
	// Readable says the sensor gave a reading at all: a fan header with nothing plugged in gives none, where a fan that has stopped reads 0.
	Readable bool `json:"readable"`
}

// Device is who made a BMC, by the number IANA gave the company, and what it says it is.
type Device struct {
	Manufacturer uint32 `json:"manufacturer"`
	Product      uint16 `json:"product"`
	Firmware     string `json:"firmware"`
}

// CompletionError is a BMC's answer that a command failed, with the code it gave: 0xc1 for a command it does not know, say.
type CompletionError struct {
	Code byte
}

func (e *CompletionError) Error() string {
	return fmt.Sprintf("the BMC answered with code %#02x (%s)", e.Code, completionWords(e.Code))
}

// completionWords is what IPMI says a completion code means.
func completionWords(code byte) string {
	switch code {
	case 0xc0:
		return "busy"
	case 0xc1:
		return "it does not know the command"
	case 0xc3:
		return "it took too long"
	case 0xc7, 0xc8:
		return "the command was the wrong length"
	case 0xc9:
		return "a value was out of range"
	case 0xcb:
		return "it has nothing to give"
	case 0xcc:
		return "a value in the command was wrong"
	case 0xd4:
		return "the login is not allowed to"
	case 0xd5:
		return "it cannot in the state it is in"
	default:
		return "a failure"
	}
}

// ErrUnreadable is what a maker whose BMCs cannot say how fast a zone is set gives back for its duty.
var ErrUnreadable = errors.New("this maker's BMCs do not say how fast a zone is set")

// Fans is one BMC's fans as its maker has them set: the part of controlling fans that each company does its own way.
type Fans interface {
	// Mode is the BMC's own fan mode now, in its maker's words: full, standard. Empty for a maker whose BMCs do not say.
	Mode(ctx context.Context) (string, error)
	// Take readies the BMC to have its zones set, remembering what it found the first time for Give. It is called before each round's settings,
	// so a BMC that has reset and taken its fans back is readied again; it says whether it had to do anything.
	Take(ctx context.Context) (bool, error)
	// Give hands the fans back to the BMC as Take first found them.
	Give(ctx context.Context) error
	// Duty is how fast a zone is set, as a percentage of full speed, or ErrUnreadable.
	Duty(ctx context.Context, zone int) (int, error)
	SetDuty(ctx context.Context, zone, duty int) error
	// Zone is the zone a fan is in, by the fan's name, by the maker's own rule; -1 for one it cannot tell.
	Zone(fan string) int
}

// Options is what the settings say of how a BMC's fans are taken over.
type Options struct {
	// FullMode puts a board that has fan modes in the one that keeps what it is set to.
	FullMode bool
}

// Maker is one company's way of setting fans.
type Maker struct {
	// Name is what the settings call it: supermicro.
	Name string
	// Tried says it has been seen to work on a real BMC. One that has not is there to be tried, and says so when it starts.
	Tried bool
	// Manufacturers are the numbers its BMCs give for who made them, so a BMC that gives another can be said to be someone else's.
	Manufacturers []uint32
	// New is the fans of a BMC of its make.
	New func(Conn, Options) Fans
}
