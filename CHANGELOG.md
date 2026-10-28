# Changelog

## Unreleased

The first release: a server's or a disk shelf's fans set through its BMC, from the temperatures of its disks and CPUs.

- **Zones follow curves.** Each zone runs as fast as its curves ask for at the temperatures it follows, between a floor and a ceiling, and comes down only once they have cooled by the hysteresis.
- **A disk shelf follows its own disks**, found by the SAS address of the enclosure they sit in, with no list of disks to keep up to date.
- **Any temperature Linux or a BMC has**: every hwmon chip, every disk through drivetemp, nvme or smartctl, and every BMC's own temperature sensors.
- **Safe when things go wrong.** A zone whose temperatures stop arriving runs at its failsafe speed; a zone its BMC changes is set again, and the correction noted with when and why, for the web page; when the program stops each zone is put back as it was found. `--dry-run` sets nothing.
- **Makers kept apart**: Supermicro, tried on a disk shelf's BMC, and Dell, not yet tried, each a small package of their own.
- **No ipmitool.** IPMI over the network and through `/dev/ipmi0`, in Go.
- **One settings file**, YAML, every mistake in it named at once, read again on SIGHUP.
- **A web page**, off unless turned on, with a light and a dark theme: each machine and shelf in turn with its zones, fans and disks, the disks shown enclosure by enclosure, an enclosure no zone follows added to one with a click, and a test that runs a zone flat out to find out which disks it cools; charts of each zone's fans, speed and temperatures, and of every disk and sensor, over up to 6 hours; every correction; a zone's curves on one chart, changed by dragging their points, with curves of their own to add for disks, chips or sensors, saved into the settings file with its comments kept; holds to run a zone quiet or flat out for a while; and the settings file to edit whole, behind a token when one is set.
- **A fan controller and nothing else**: it publishes no metrics, leaving those to an IPMI exporter, and without its web page opens no port and reads only what its zones follow. `ipmi-fan-controller healthy` is the image's health check.
