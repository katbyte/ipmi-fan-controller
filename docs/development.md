# Development

```bash
make            # format and build
make test       # the tests, under the race detector
make testacc    # the live tests: the image, as docker runs it
make cover      # the tests with coverage
make check-all  # build, tests, live tests, every linter and the dependency check
make tools      # install the pinned dev tools into .tools/bin
```

Dependencies are vendored. The dev tools are pinned in `.tools/go.mod` and the makefile.

## Where things are

| Path | What it does |
|---|---|
| `lib/config` | the settings file: reading it, checking it, and saving a change into it with its comments kept |
| `lib/sensors` | the machine's temperatures: hwmon chips, disks and the enclosures they sit in, and smartctl |
| `lib/bmc` | talking to a BMC, over the network or through `/dev/ipmi0`, and what every maker has to do |
| `lib/bmc/supermicro`, `lib/bmc/dell` | each maker's own commands |
| `lib/bmc/makers` | the list of makers |
| `lib/control` | the rounds: reading, deciding each zone, setting it, and handing the fans back |
| `lib/web` | the web page and what it asks |
| `cli` | the commands and settings |
| `acceptance` | the live tests of the image |

The rest is shared with the other tools, from [go-kt](https://github.com/katbyte/go-kt): the logger, the version and the HTTP server.

## The tests

`make test` needs no BMC and sets no real fan.

- `lib/bmc/bmctest` is a made-up Supermicro board: it answers the fan commands, reads the sensors a test gives it, and can be made to reset, set its own fans as a mainboard does, or stop answering. It can be served on the network, speaking IPMI 2.0, which is what the program's own IPMI is tested against.
- `lib/sensors/sensorstest` lays out a `/sys` as the kernel does: chips, SAS disks in enclosures with drivetemp, NVMe disks, and disks only smartctl can read.
- So the commands' tests run the program whole: `serve` sets a made-up shelf from made-up disks, takes up a curve changed on the web page, opens no port without the web page, is healthy while it runs, and puts the shelf back when stopped.

## The live tests

`make testacc` builds the image and runs it against a made-up shelf the test serves on the network, reached from inside the container as `host.docker.internal`. It needs `docker` and takes a few seconds.

- The image logs in to the shelf and sets it from its temperature, with no port open.
- It passes its own health check and does not run as root.
- With the web page on, it serves the page, showing the shelf and its fans.
- `docker stop` has it put the shelf back as it found it.
- Given settings with a mistake in them, it stops at once and names the mistake.

## What the tests cannot reach

- **Real BMCs.** How a real one answers, takes its fans back after a reset, or refuses a command, is only seen on one. The Supermicro commands were tried by hand on one disk shelf's BMC.
- **A real machine's `/sys`.** The made-up one is laid out from the kernel's own layout and from what data's `/sys` showed; a capture from a real machine is still to come.
