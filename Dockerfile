# build with the vendored deps, then ship the static binary on alpine: small, but keeps a shell
# so you can `docker exec -it ipmi-fan-controller sh` to poke at things. runs as a non-root user.
# make docker passes VERSION/COMMIT from git; a bare `docker build .` reports "dev".
#
# REGISTRY is where the two base images come from: Google's mirror of Docker Hub, which holds
# the same images, unless a build says otherwise. Docker Hub itself (docker.io/library) limits
# how much an address may pull, which a CI runner's shared address runs into; CI builds again
# from it when the mirror gives nothing.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24
ARG REGISTRY=mirror.gcr.io/library

FROM ${REGISTRY}/golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor \
      -ldflags "-s -w \
        -X github.com/katbyte/go-kt/version.Version=${VERSION} \
        -X github.com/katbyte/go-kt/version.GitCommit=${COMMIT}" \
      -o /ipmi-fan-controller .

FROM ${REGISTRY}/alpine:${ALPINE_VERSION}
# smartctl asks the disks the kernel gives no temperature for; it needs the container run as root
# with the disks given to it, and is left unused otherwise
RUN apk add --no-cache ca-certificates tzdata smartmontools \
    && adduser -D -H -u 65532 fans \
    && mkdir /config && chown fans /config
COPY --from=build /ipmi-fan-controller /usr/local/bin/ipmi-fan-controller
USER fans
# the settings file is kept in /config, a volume the web page can write it back to. /sys is the
# machine's own in any container, so its temperatures need nothing mounted; its own BMC needs
# /dev/ipmi0 given to the container, and root. The port is the web page's, which is off unless
# IPMI_FAN_CONTROLLER_WEB is set: without it nothing listens. The health check asks the program
# whether its rounds are still going, with no port needed.
ENV IPMI_FAN_CONTROLLER_CONFIG=/config/ipmi-fan-controller.yaml
ENV IPMI_FAN_CONTROLLER_LISTEN=:9126
VOLUME /config
EXPOSE 9126
HEALTHCHECK --interval=30s --timeout=3s --start-period=1m \
  CMD ["ipmi-fan-controller", "healthy"]
# a container is stopped with SIGTERM, which has the program hand the fans back before it ends:
# give it the time to (docker stop -t, or stop_grace_period in compose)
STOPSIGNAL SIGTERM
ENTRYPOINT ["ipmi-fan-controller"]
CMD ["serve"]
