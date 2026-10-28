# Makers

Reading a BMC's sensors is the same on every BMC. Setting its fans is not: each maker has commands of its own, and each maker's are kept to one package under `lib/bmc`. Supermicro is the default.

## Supermicro

Tried on a Supermicro 847 disk shelf's BMC (firmware 1.33, product 0xffff) on 2026-10-20, by hand with ipmitool, then the same commands from this program's tests.

| What | Command |
|---|---|
| read the fan mode | `raw 0x30 0x45 0x00` |
| set the fan mode | `raw 0x30 0x45 0x01 <mode>`: 0 standard, 1 full, 2 optimal, 4 heavy IO |
| read a zone's speed | `raw 0x30 0x70 0x66 0x00 <zone>`, in percent |
| set a zone's speed | `raw 0x30 0x70 0x66 0x01 <zone> <0 to 100>` |

- **Disk shelves.** The shelf sets its own speed as its temperatures move, with no reset: set to 50% by hand, it kept that for hours, then went to 60% overnight as the shelf warmed. The program reads each zone's speed every round and sets it again, so a speed the shelf changes is back within one interval. Zone 0 runs all its fans; zone 1 did nothing. Its mode read 0 while its own web page said Full, so the mode numbers may not mean on a shelf what they mean on a mainboard: the program shows the number beside the name, and never sets a shelf's mode unless `full_mode` is set. Whether Full stops a shelf setting its own speed is not yet tried.
- **Mainboards.** A mainboard in any mode but Full sets its fans itself, and takes back a speed set by hand within seconds. `full_mode: true` puts it in Full while the program runs, and back in the mode it was found in when it stops. Not yet tried on a real mainboard.
- **Resets.** A BMC that resets goes back to its own mode and speeds. The program reads each zone's speed every round: one that is not what it set is set again, and a board that has left Full mode is put back in it. Each correction is noted, with when and why, on the web page's Corrections tab.
- **Fans with no reading.** A shelf's fan headers with nothing plugged in give no reading, which is not a stopped fan. A fan that reads 0 is one, and is flagged: a shelf's BMC with no lower limits set for its fans says nothing of it.

## Dell

Not yet tried. An iDRAC sets every fan together, as zone 0, and does not say how fast that is.

| What | Command |
|---|---|
| take the fans | `raw 0x30 0x30 0x01 0x00` |
| set every fan | `raw 0x30 0x30 0x02 0xff <0 to 100>` |
| hand them back | `raw 0x30 0x30 0x01 0x01` |

Dell took these away in iDRAC 9 firmware 3.34 and later, which refuse them: the program then says the BMC refused, and sets nothing.

## Adding a maker

A maker is a package beside `supermicro` and `dell` with a `bmc.Maker` in it, and a line in `lib/bmc/makers`. The part it does is small:

| Method | What it does |
|---|---|
| `Mode` | the BMC's own fan mode, in the maker's words, or nothing for a BMC that does not say |
| `Take` | readies the BMC to have its fans set by hand, remembering what it found; called every round, so a BMC that reset is readied again |
| `Give` | hands the fans back as `Take` found them |
| `Duty`, `SetDuty` | a zone's speed, in percent; `bmc.ErrUnreadable` for a BMC that cannot say |
| `Zone` | which zone a fan is in, by its name |

`bmctest` has a made-up Supermicro board to test against, in-process or on the network. A maker no one has tried on a real BMC is marked so, and the program says so when it starts.

Others found, and not tried: ASRock Rack boards take `raw 0x3a 0x01` with a byte for each fan, and keep at least 20%; the number of fans differs from board to board.
