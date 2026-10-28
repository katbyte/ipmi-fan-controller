# Contributing

Issues and pull requests are welcome.

- `make check-all` runs everything CI runs. See [development](docs/development.md).
- A BMC that answers differently from the ones tried is worth an issue: say who made it, its firmware, and what `ipmi-fan-controller status` printed.
- A new maker is a package of its own: see [adding a maker](docs/makers.md#adding-a-maker).

## Releases

- Changes collect under "Unreleased" in the [changelog](CHANGELOG.md).
- A version is tagged when a batch is worth upgrading for.
- Breaking changes are grouped into one release, not shipped one at a time.
- Pushing a `v*` tag publishes the programs, the Docker image and the Homebrew formula.
