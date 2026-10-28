# Running it in a container

[`docker-compose.yml`](../docker-compose.yml) runs the released image. It keeps the settings file in `./config`, which the web page writes back to.

```yaml
volumes:
  - ./config:/config
```

- **Temperatures need nothing mounted.** `/sys` in a container is the machine's own, so its chips and disks are read as they are outside one, by a user that is not root.
- **A BMC on the network needs nothing either.** A disk shelf's BMC, or a VM's host's, is reached over the network from inside the container.
- **The machine's own BMC needs `/dev/ipmi0`, and root.** Linux's IPMI device is root's alone: give the container the device and run it as root (`user: "0"`).
- **`smartctl` needs root and the disks.** Only a disk the kernel gives no temperature for is asked with it.
- **`./config` has to be writable by the image's user**, 65532, for the web page to save the settings, or the container run as root.
- **No port, unless the web page is on.** Publish `9126` only to use the page. The health check is `ipmi-fan-controller healthy`, run inside the container, and needs no port.
- **Stopping it hands the fans back.** `docker stop` sends SIGTERM, and the program puts each zone back as it found it before it ends. The compose file gives it a minute (`stop_grace_period`); `docker stop` alone gives ten seconds.

## In a VM

A VM has no `/dev/ipmi0`, and often no CPU temperature of its own: data, a TrueNAS VM, has neither. It can still follow its disks, which it sees, and its host's CPU through the host's BMC, `ipmi/<host>/CPU Temp`, reached over the network like a disk shelf's.

## Checking it first

```bash
docker run --rm -v ./config:/config ghcr.io/katbyte/ipmi-fan-controller:latest status
```

`status` reads everything once and sets nothing: every source, BMC, fan and temperature, and what each zone would be set to.
