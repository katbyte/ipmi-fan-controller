// Package main implements ipmi-fan-controller, which sets fan speeds through a server's or disk shelf's BMC from its disk and CPU temperatures.
package main

import (
	"os"

	"github.com/gookit/color"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/ipmi-fan-controller/cli"
)

func main() {
	// the log level comes from IPMI_FAN_CONTROLLER_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("IPMI_FAN_CONTROLLER_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(color.Sprintf("<red>ipmi-fan-controller: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(color.Sprintf("<red>ipmi-fan-controller:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
