# Settings

The settings file is YAML. It is `--config` (`IPMI_FAN_CONTROLLER_CONFIG`), or the first there of `./ipmi-fan-controller.yaml`, `~/.ipmi-fan-controller.yaml` and `/etc/ipmi-fan-controller.yaml`. With none, the program reads everything and sets nothing; the web page writes a new one to `~/.ipmi-fan-controller.yaml`.

Every mistake is said at once, each with where it is: a setting that is not one, `flor: 20` say, is a mistake, since it would otherwise be left at its default without a word.

## The top level

| Setting | Default | What it sets |
|---|---|---|
| `interval` | `10s` | how often temperatures are read and fans set |
| `disk_interval` | `1m` | how often disks are asked their temperature: a disk warms over minutes, and asking costs it a command |
| `hysteresis` | `2` | how many degrees a temperature has to fall before the fans slow down, so they do not hunt around one point |
| `smartctl` | `auto` | what asks the disks the kernel gives no temperature for: `auto` runs `smartctl` when it is there, `off` never, or a path to one |
| `bmcs` | none | the BMCs |
| `zones` | none | the zones of fans to set |

## BMCs

```yaml
bmcs:
  - name: tower                   # the machine's own, through /dev/ipmi0
    full_mode: true
  - name: annex                   # a disk shelf's, on the network
    host: annex-ipmi.example.net
    user: ADMIN
    password: ${ANNEX_PASSWORD}
```

| Setting | Default | What it sets |
|---|---|---|
| `name` | | what the BMC is called: letters, numbers, `-` and `_` |
| `maker` | `supermicro` | how its fans are set: [the makers](makers.md) |
| `host` | none | where it is on the network. Left out, it is the machine's own, through Linux's IPMI driver |
| `port` | `623` | |
| `user`, `password` | | the login, for one on the network. It needs to be allowed to send its maker's own commands: an administrator on a Supermicro BMC |
| `password_file` | | read the password from a file, a container's secret say |
| `full_mode` | `false` | put a Supermicro mainboard in its Full fan mode while setting its fans. In any other mode a mainboard sets them itself, and takes back what it was set to within seconds. A disk shelf's BMC also changes its speed as its temperatures move; without `full_mode` the program sets it again each round, and whether Full stops a shelf doing so is not yet tried |
| `on_stop` | `restore` | what is left when the program stops: `restore` puts each zone back at the speed it was found at and the BMC back in the mode it was in; `full` leaves each zone at full speed |

`${NAME}` in `host`, `user`, `password` and `password_file` is that environment variable, and one that is not set is a mistake. A file the web page saves keeps the `${NAME}`, not what it stands for.

With no BMCs in the settings, the machine's own is read when it has one, as `local`, and nothing is set.

## Zones

A zone is fans a BMC sets together. Supermicro mainboards have zone 0 for the numbered fans, FAN1 and on, and zone 1 for the lettered ones, FANA and on. A Supermicro disk shelf runs all its fans from zone 0.

```yaml
zones:
  - name: shelf
    bmc: annex
    zone: 0
    floor: 30
    ceiling: 100
    failsafe: 100
    follow:
      - name: disks
        sensors: [disk/5003048000f8a93f/*]
        curve: {35: 30, 45: 50, 52: 100}
      - name: sas chips
        sensors: ["ipmi/annex/SAS3 * Temp"]
        by: average
        curve: {70: 30, 90: 100}
```

| Setting | Default | What it sets |
|---|---|---|
| `name` | | what the zone is called |
| `bmc`, `zone` | | the BMC it is on, and that BMC's number for it |
| `floor` | `30` | the slowest it runs, in percent of full speed, whatever its curves say |
| `ceiling` | `100` | the fastest it runs on its curves |
| `failsafe` | `100` | how fast it runs when what it follows stops answering |
| `fans` | the maker's rule | the fans in it, for the web page: `[FAN1, FAN2]` |
| `follow` | | what decides its speed |

## Follows

Each follow asks for a speed from its own temperatures, and the zone runs at the fastest asked for, between its floor and ceiling. So a zone can follow its disks on one curve and its CPU on another.

| Setting | Default | What it sets |
|---|---|---|
| `name` | its sensors | what it is called |
| `sensors` | | the temperatures followed, by name, with `*` for any part of one. `status` lists every name |
| `by` | `hottest` | how they make one temperature: `hottest`, or `average` |
| `curve` | | temperature: speed pairs, `{35: 30, 45: 50, 52: 100}`. Between two points it is a straight line; under the coolest point it is that point's speed, and over the hottest, that one's. A curve has to rise or stay level as it gets hotter |

A curve is run upwards at once, and downwards only once the temperature has fallen by the hysteresis. At 47° on the disks' curve above the shelf runs at 65%, and it does not slow down until the disks are under 45°.

## When a follow cannot be followed

- **A source stops answering.** A source is the machine's chips (`hwmon`), its disks (`disks`), or one BMC's sensors (`ipmi/<bmc>`). What it last said is followed for three of its intervals. After that every follow that reads it fails, and its zone runs at its failsafe speed until the source answers again.
- **A sensor is gone.** A disk that is pulled, or a sensor that cannot be read, is left out and the rest are followed. The web page and `status` list what could not be read.
- **Nothing matches.** A follow that matches no sensor at all fails, and its zone runs at its failsafe speed.
- **The BMC stops answering.** Its zones cannot be set, and stay where they were. The program logs in again each round.

## What is read

Each round reads only what the zones follow, and what setting them needs: each zone's speed, and a Supermicro board's mode when `full_mode` is set. A source no zone follows is not read at all, and a disk no zone follows is not asked its temperature. With the web page on, and for `status`, everything is read, for them to show: every chip, disk and BMC sensor, every fan, and each BMC's fan mode.

## Holds

The web page can hold a zone quiet, at a speed of its own, or at full speed, for a while. A quiet hold is never under the floor, and gives way as soon as a temperature reaches the top of its curve. Holds are not kept when the program restarts.

## Examples

A server's own fans, CPU fans on the CPU and the rest on the disks, with the board in Full mode:

```yaml
bmcs:
  - name: tower
    full_mode: true
zones:
  - name: cpu
    bmc: tower
    zone: 0
    follow:
      - sensors: [hwmon/k10temp/Tctl]
        curve: {45: 30, 70: 60, 85: 100}
  - name: disks
    bmc: tower
    zone: 1
    follow:
      - sensors: [disk/5003048000c4d53f/*, disk/5003048000d6e73f/*]
        curve: {35: 30, 45: 50, 52: 100}
```

A server in a VM, with no `/dev/ipmi0`, reaching its host's BMC and its disk shelf's over the network: give each a `host`, and the rest is the same.
