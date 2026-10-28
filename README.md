# ipmi-fan-controller

[![GitHub release](https://img.shields.io/github/v/release/katbyte/ipmi-fan-controller?color=blueviolet)](https://github.com/katbyte/ipmi-fan-controller/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/ipmi-fan-controller?color=00ADD8)](https://github.com/katbyte/ipmi-fan-controller/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/ipmi-fan-controller?color=blue)](https://github.com/katbyte/ipmi-fan-controller/blob/main/LICENSE)
![build](https://github.com/katbyte/ipmi-fan-controller/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/ipmi-fan-controller/actions/workflows/pr-tests.yaml/badge.svg)
![acceptance](https://github.com/katbyte/ipmi-fan-controller/actions/workflows/pr-acceptance.yaml/badge.svg)
![lint](https://github.com/katbyte/ipmi-fan-controller/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/ipmi-fan-controller/badges/coverage.json)](https://github.com/katbyte/ipmi-fan-controller/actions/workflows/coverage.yaml)

Sets the fans of a server or a disk shelf through its BMC, from the temperatures of its disks and CPUs.

A BMC's own fan modes follow its own sensors, and a disk shelf's BMC often offers only "loud" and "louder". This follows what matters instead: each zone of fans runs as fast as a curve asks for at the temperatures it follows, the hottest disk in that shelf say, and no faster. A disk shelf's fans follow only the disks in that shelf, found by the shelf's SAS address, with no list of disks to keep up to date.

**How it behaves.**

- It reads before it writes, and never runs a zone below its floor.
- When what a zone follows stops answering, the zone runs at its failsafe speed, full unless set otherwise.
- When a BMC changes a zone, by itself or after a reset, the zone is set again, and the correction is noted with when and why.
- When it stops, each zone goes back to the speed it was found at, and a Supermicro board back to the fan mode it was in.
- `--dry-run` sets nothing and says what it would set.
- If it is killed outright, or its machine stops, the fans stay where they were last set: choose floors with that in mind.
- It is a fan controller and nothing else. It publishes no metrics: fan speeds, temperatures and the rest are for an IPMI exporter, [ipmi_exporter](https://github.com/prometheus-community/ipmi_exporter) say. Without its web page it opens no port, and reads only what its zones follow.

## Install

```bash
docker pull ghcr.io/katbyte/ipmi-fan-controller:latest     # or
go install github.com/katbyte/ipmi-fan-controller@latest   # or
brew install katbyte/tap/ipmi-fan-controller               # Homebrew
```

or a ready-built program from the [releases](https://github.com/katbyte/ipmi-fan-controller/releases/latest). It runs on Linux, where the disks are. The macOS build can only check BMCs on the network: the machine's own temperatures are Linux's.

It needs nothing else: no ipmitool, and no C library. It talks IPMI itself, to the machine's own BMC through Linux's IPMI driver (`/dev/ipmi0`) and to any other over the network.

## Quick start

See what there is to follow before setting anything up. With no settings file it reads everything, including the machine's own BMC when it has one, and sets nothing:

```bash
ipmi-fan-controller status
```

Then write `ipmi-fan-controller.yaml`. This one runs a disk shelf's fans from the hottest disk in it:

```yaml
bmcs:
  - name: annex
    host: annex-ipmi.example.net
    user: ADMIN
    password: ${ANNEX_PASSWORD}         # from the environment

zones:
  - name: shelf
    bmc: annex
    zone: 0
    floor: 30                           # never slower than 30%
    follow:
      - name: disks
        sensors: [disk/5003048000f8a93f/*]    # every disk in the shelf with that SAS address
        curve: {35: 30, 45: 50, 52: 100}      # °C: % of full speed, straight lines between
```

```bash
ipmi-fan-controller status              # what it would set now, and why
ipmi-fan-controller serve --dry-run     # say what it would set every round, and set nothing
ipmi-fan-controller serve --web         # set the fans, with the web page at :9126
```

[Every setting](docs/settings.md), with more examples.

## The web page

`--web` (or `IPMI_FAN_CONTROLLER_WEB=true`) serves a page at `/` on `:9126`. It is off unless turned on. Only while it is on does the program read the temperatures, fans and fan modes no zone follows, for the page to show.

- **One machine or shelf at a time.** A button for each BMC, the main machine and each disk shelf, with its zones' speeds and its hottest disk, picks the one shown: what it says it is, its fan mode and every fan, then a wide row for each of its zones: its speed now and the speed it wants and why, how often it has been corrected, what it follows and how hot that is, and its curves.
- **Charts.** Each zone's fans, and its speed beside the temperatures it follows, over the last 15 minutes to 6 hours. Every disk and sensor too, with the value of each at any moment under the pointer. They are kept in memory while the web page is on, and start again when the program does: for longer, an IPMI exporter's readings are the place.
- **Which disks each zone's fans cool.** Each machine and shelf shows the disks its zones follow, enclosure by enclosure in slot order, each coloured by how hot it is; the main machine shows the disks in no enclosure too, which are inside it. Disks no zone follows are shown on their own, under "followed by no zone", so a shelf left out is plain to see, each enclosure with a button to have a zone follow its disks: they are added to the zone's follows in the settings file.
- **Finding out.** "Find out which disks it cools" runs a zone flat out for 10 to 30 minutes, then shows how much every disk cooled, enclosure by enclosure, most first. One test runs at a time, and none in a dry run; the zone goes back to its curve after.
- **Changing a curve.** A zone's curves are drawn together on one chart, each with where its temperature is now. Pick one from the list to change it on the chart: click to add a point, drag one to move it, and double-click or right-click one to take it away. A curve only rises as it gets hotter, so a point is kept between its neighbours. The same list adds a curve of its own for an enclosure's disks, this machine's chips or one of the BMC's sensors, and "take it away" removes one. Floor, ceiling and failsafe, and each point as numbers, are under the zone's limits. Nothing changes until Save: a change is then checked, written into the settings file with its comments kept, and taken up at once.
- **Holds.** "Hold quiet" runs a zone at a speed of your choosing for a while: 2 hours say. It gives way when a temperature reaches the top of its curve. "Full speed" runs a zone flat out. Either ends when its time is up, or with "Let go".
- **Corrections.** Each time a zone was found at another speed than it was set to and set again: when, what it was found at, what it was set back to, the BMC's fan mode, and why, its BMC changing it or resetting. How many there have been, in the last hour and an hour on average. Kept while the program runs, the latest 100 for each zone.
- **Settings.** The whole settings file, to edit and save. A file with anything wrong in it is not saved.

`IPMI_FAN_CONTROLLER_WEB_TOKEN` sets a token the page asks for before it changes anything. Without one, anyone who can reach the page can change the fans, and the program says so when it starts.

## What it follows

Every temperature has a name, and a zone follows names, with `*` for any part of one. `status` lists them all.

| Name | What it is |
|---|---|
| `disk/<enclosure>/<serial>` | a disk, in the enclosure with that SAS address, the id label sas-exporter gives it, or `disk/none/<serial>` for one in no enclosure |
| `hwmon/<chip>/<label>` | anything the Linux kernel's hwmon has: `hwmon/k10temp/Tctl` for an AMD CPU, `hwmon/coretemp/Package id 0` for an Intel one, a board's own sensors |
| `ipmi/<bmc>/<sensor>` | a BMC's own temperature sensors: `ipmi/tower/CPU Temp`, `ipmi/annex/SAS3 F1 Temp` |

Disk temperatures come from the kernel's `drivetemp` and `nvme` drivers, which need no root. A disk they do not cover is asked with `smartctl`, which needs root, and a disk that is spun down is left asleep.

## Makers

The commands that set fans differ from one maker to the next, and each maker's are kept to one small package. Supermicro is the default.

| Maker | Tried | On |
|---|---|---|
| `supermicro` | yes | a Supermicro 847 disk shelf's BMC (firmware 1.33) |
| `dell` | not yet | an iDRAC; Dell took the commands away in iDRAC 9 firmware 3.34 and later |

[How each is driven, and adding another](docs/makers.md).

## Settings

The settings file holds the BMCs, the zones and what they follow: [every setting](docs/settings.md). The program's own settings are flags or environment variables:

| Variable | Flag | Default | What it sets |
|---|---|---|---|
| `IPMI_FAN_CONTROLLER_CONFIG` | `--config` | the first there of `./ipmi-fan-controller.yaml`, `~/.ipmi-fan-controller.yaml` and `/etc/ipmi-fan-controller.yaml` | the settings file |
| `IPMI_FAN_CONTROLLER_LISTEN` | `--listen` | `:9126` | the address the web page is served on |
| `IPMI_FAN_CONTROLLER_WEB` | `--web` | off | serve the web page |
| `IPMI_FAN_CONTROLLER_WEB_TOKEN` | `--web-token` | none | what the web page needs before it changes anything |
| `IPMI_FAN_CONTROLLER_DRY_RUN` | `--dry-run` | off | set nothing, and say what would be set |
| `IPMI_FAN_CONTROLLER_ROOT` | `--root` | `/` | where the machine's own `/` is, for its `/sys` |
| `IPMI_FAN_CONTROLLER_LOG` | | `WARN` | log level: `INFO` says each change it makes |

`kill -HUP` reads the settings file again. Settings with a mistake in them are not taken up, and the mistake is logged.

## Is it working

- **The log.** At `INFO` it says each speed it sets and why, each zone that fails safe or is taken back by its BMC, and each BMC that stops answering.
- **`ipmi-fan-controller healthy`** says whether `serve` is running and its rounds are going on. It is the image's health check, and needs no port.
- **The web page**, when it is on, shows all of it.
- **Fan speeds over time** are an IPMI exporter's to record, and a stopped fan its to alert on: the program logs one only while the web page has it reading the fans.

## How this is built

- **Tested over the wire.** The program's own IPMI is tested against a made-up BMC, speaking IPMI 2.0 over the network as a real one does: logging in, listing sensors, reading them, and Supermicro's fan commands.
- **Tested against made-up machines.** The tests lay out a `/sys` as the kernel lays out a real one: chips, SAS disks in enclosures, NVMe disks, and disks only `smartctl` can read.
- **The image is tested as it ships.** Live tests start the Docker image as its own user, have it set a made-up shelf on the network, check its health and its web page, and stop it, checking the shelf is put back. CI fails if they fail.
- **What no test reaches.** Real BMCs. Supermicro's commands were tried by hand on one disk shelf's BMC; a mainboard's Full mode and Dell's commands have not been tried.
- **Checks CI runs.** Unit tests under the race detector, and the live tests. golangci-lint, CodeQL, govulncheck and a scan of the built program. Linters for the workflows, shell scripts, YAML, markdown and spelling.

## More

- [Every setting](docs/settings.md)
- [Makers, and adding one](docs/makers.md)
- [Running it in a container](docs/container.md)
- [Development and tests](docs/development.md)
- [Contributing and releases](CONTRIBUTING.md)

Prior art: [smfc](https://github.com/petersulyok/smfc) does much the same for Supermicro boards, in Python, with ipmitool.
