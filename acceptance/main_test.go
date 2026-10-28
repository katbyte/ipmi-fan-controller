//go:build acceptance

// Package acceptance_test runs the image a deployment runs, as docker runs it, against a made-up disk shelf's BMC that the test itself serves on the
// network, speaking IPMI 2.0 as a real one does. The unit tests run the program in the test's own process; these are where the built image gets
// to run as a container does: as its own user and not root, with its settings in /config, reaching a BMC over the network from inside the
// container, and handing the fans back when docker stops it.
//
// No real BMC is needed or touched. What a real one does that the made-up one does not, take its fans back after a reset say, is only seen on one.
//
//	make testacc                                                         # builds the image and runs these against it
//	IPMI_FAN_CONTROLLER_TEST_PROGRAM_IMAGE=ipmi-fan-controller make test-live  # or against one already built
package acceptance_test
