# Contributing

Thanks for your interest in netbox-groundtruth-agent.

## Before you start

- For anything bigger than a small fix, open an issue first to agree on the approach.
- Keep the core rule in mind: the agent writes **observed facts** only, never design intent (site, role, IPs, names, …), and it must never delete what it did not create.

## Development

```sh
go test ./...
go test -tags nvml,iap ./...    # needs cgo
golangci-lint run ./...
```

- Collection logic is tested with the fixture tree in `testdata/rootfs`; NetBox interaction with the fake server in `internal/netbox/netboxtest`. Add a test for every behaviour change.
- **No real data in the repository.** Fixtures must use made-up serial numbers, MAC addresses from `00:00:5E:00:53:00/24` (RFC 7042 documentation range), `example.com` names and documentation IP ranges. Never include internal host names, URLs, organisation names or custom field/tag names from a real deployment.
- Code comments, documentation and CLI text are in English.
- Update `CHANGELOG.md` under "Unreleased".

## Pull requests

- One topic per pull request, with a description of the motivation.
- CI (lint, tests, build) must pass.
- Every commit must be signed off under the [Developer Certificate of Origin](https://developercert.org/) (DCO). Use `git commit -s`, which adds a `Signed-off-by: Your Name <you@example.com>` line. The sign-off certifies that you wrote the change or otherwise have the right to submit it under the project's license (Apache-2.0). No separate contributor agreement is required.

## Releases

Maintainers tag `vX.Y.Z` (semantic versioning). The release workflow builds the binaries and `.deb` packages, then signs the checksums and attaches SBOMs.
