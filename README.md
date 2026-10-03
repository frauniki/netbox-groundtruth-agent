# netbox-groundtruth-agent

A NetBox agent that records the hardware a server actually has (serial numbers, CPUs, memory modules, disks, NICs and MAC addresses, GPUs) in NetBox.

NetBox is your **source of truth**: the intended state of the infrastructure, which DHCP, DNS and device configuration are generated from. This agent reports the **ground truth**: what was observed on the machine itself. It writes only observed facts and leaves every design decision to people.

> This project is not related to [Solvik/netbox-agent](https://github.com/Solvik/netbox-agent) (Python). It is a separate implementation in Go, with a different scope and safety model.

## Design: write facts, never intent

Mistakes in a source of truth turn into outages, so the agent splits NetBox data into two kinds:

| Kind | Examples | Agent |
|---|---|---|
| **Observed facts** | serial, CPU, memory, disks, NICs and MACs, GPUs | writes them |
| **Design intent** | site, rack, role, status, platform, name, IP assignments, device type | never writes them; reports mismatches |

The agent also behaves as follows:

- **It finds the device, never creates it.** It looks the device up by serial number. With no match it reports `unregistered`; with several it reports `duplicate`. In both cases it changes nothing.
- **It is a dry run by default.** `apply` refuses to run without `-confirm`.
- **It deletes only what it owns.** Inventory items it creates carry an owner tag. It deletes only tagged items that are no longer observed. Items a person added (untagged) are never modified or deleted.
- **Not collected is not the same as gone.** If a section (for example memory modules, when the SMBIOS table is unreadable) could not be collected, nothing in that section is deleted.
- **It stops instead of guessing.** Any NetBox error, or a list response that doesn't add up, aborts the run before anything is written. Plans with more changes than `sync.max_changes`, or more deletions than `sync.max_deletes`, are not applied.
- **It is idempotent.** A second run with the same hardware plans zero changes.

### Architecture

```
Source (local: sysfs, SMBIOS, NVML)  →  Snapshot (NetBox-independent)
       →  Planner (reads NetBox, computes a Plan)  →  Sink (netbox: REST API | dryrun)
```

`internal/source/talos` (collect Talos nodes through the Talos API) and `internal/sink/diode` (send to NetBox Diode) are placeholders for later phases.

## What is collected and written

| Observed | Source on the machine | Written to NetBox |
|---|---|---|
| Manufacturer, product | `/sys/class/dmi/id` | not written; reported if it differs from the device type |
| Serial number | `/sys/class/dmi/id/product_serial`, falls back to `board_serial` | used to find the device |
| BIOS version | `/sys/class/dmi/id/bios_version` | custom field |
| OS and kernel | `/etc/os-release`, `/proc/sys/kernel/osrelease` | custom field (the platform is not touched) |
| CPU model, sockets, cores, threads | `/proc/cpuinfo`, `/sys/devices/system/cpu/*/topology`, SMBIOS type 4 on arm64 | custom fields, plus one inventory item per socket |
| Memory modules: slot, size, type, speed, vendor, part number, serial | SMBIOS type 17 (`/sys/firmware/dmi/tables/DMI`) | inventory items; total as a custom field |
| Disks: name, model, serial, size, transport | `/sys/block` (serial from `device/serial` or SCSI VPD page 80h) | inventory items |
| Physical NICs: name, MAC, speed, driver, PCI address | `/sys/class/net` | interface MAC addresses, inventory items |
| GPUs: model, serial, UUID, PCI address, VBIOS, driver | NVML; without NVML, the PCI device list and `pci.ids` (no serial) | inventory items; model and count as custom fields |
| IP addresses | the host's interfaces | not written; differences are reported |

Virtual interfaces (bridge, bond, VLAN, veth, docker, tun, …), SR-IOV virtual functions, non-Ethernet links (for example InfiniBand) and, by default, USB NICs are ignored. Bond members report their permanent MAC address. Disks can be excluded by name, transport, model and removability (see `collect.disks`).

### Interfaces and MAC addresses

For each observed NIC the agent tries, in this order:

1. An interface whose MAC address matches. Nothing to do; a different name is only reported.
2. An interface with the same name and no MAC address. The MAC address is added.
3. Otherwise, with `sync.create_interfaces: true`, a new interface is created. Its type is guessed from the link speed (`sync.interface_types`); if the speed is unknown, `sync.default_interface_type` is used and a warning is logged. The default is not to create interfaces.

Interfaces are never renamed or deleted, and cables are never touched.

### Inventory items

Inventory items are named `CPU <socket>`, `Memory <slot>`, `Disk <name>`, `GPU <pci address>` and `NIC <name>`. They are matched by serial number first, then by name. Created items get `discovered: true` and the owner tag (`sync.owner_tag`, default `managed-by-groundtruth`). **Create the tag in NetBox first**; without it the agent skips inventory items and logs a warning.

### Custom fields

Values are written to existing device custom fields. Unknown fields are skipped with a warning. Load [`examples/custom-fields.yaml`](examples/custom-fields.yaml) through *Customization → Custom Fields → Import* to create them. You can rename them in `sync.custom_fields`, or disable one by setting it to `""`.

| Key (default field name) | Type | Value |
|---|---|---|
| `cpu_model` | text | CPU model name |
| `cpu_sockets` | integer | populated sockets |
| `cpu_cores` | integer | physical cores, all sockets |
| `cpu_threads` | integer | logical CPUs (online) |
| `memory_total_gb` | integer | GiB; SMBIOS module total, else `MemTotal` |
| `gpu_model` | text | distinct GPU models, comma separated |
| `gpu_count` | integer | number of GPUs |
| `bios_version` | text | BIOS version |
| `os_version` | text | e.g. `Ubuntu 24.04.1 LTS (kernel 6.8.0-45-generic)` |
| `last_collected` | datetime (or text) | time of the last `apply`; not counted as a change |

## Supported NetBox versions

NetBox **4.0 and later**. The agent reads `/api/status/` and adapts:

- **4.0 – 4.1**: the MAC address is set on the interface (`mac_address`).
- **4.2 and later**: MAC addresses are separate objects. The agent creates a `dcim.macaddress` assigned to the interface and sets it as the interface's `primary_mac_address`.

Both v1 (`Token …`) and v2 (`nbt_…`, sent as `Bearer …`, NetBox 4.5+) API tokens work.

The behaviour was checked against the NetBox 4.7 source code and an in-memory imitation of the API used by the test suite. It has not yet been tested against a live NetBox instance (see [Open items](#open-items)).

## Required permissions

Give the agent its own user and token. A NetBox object permission can't be limited to custom fields, so limit what the token can see with constraints (for example by site or tenant) where possible.

| Object type | Actions | Why |
|---|---|---|
| DCIM › Device | view, change | find the device, write custom fields |
| DCIM › Interface | view, add, change | match NICs, set MAC addresses, optional creation |
| DCIM › MAC Address (4.2+) | view, add | MAC address objects |
| DCIM › Inventory Item | view, add, change, delete | parts inventory |
| Extras › Tag | view | look up the owner tag |
| IPAM › IP Address | view | report IP differences |

`add` on interfaces is only needed with `sync.create_interfaces: true`.

## Installation

Download a release from GitHub: a `.deb` for Ubuntu, or a tarball for `amd64` and `arm64`. Release binaries are built on Ubuntu 22.04 and need glibc 2.35 or newer.

```sh
sudo apt install ./groundtruth-agent_<version>_linux_amd64.deb
sudoedit /etc/groundtruth-agent/config.yaml            # set netbox.url
sudo install -m 600 /dev/null /etc/groundtruth-agent/token
sudoedit /etc/groundtruth-agent/token                  # paste the API token
sudo groundtruth-agent collect                         # check what is observed
sudo groundtruth-agent plan -config /etc/groundtruth-agent/config.yaml
```

### Verifying a release

`checksums.txt` is signed with cosign (keyless, GitHub OIDC). SBOMs (SPDX) are attached to every release.

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/frauniki/netbox-groundtruth-agent/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
```

### Building from source

```sh
go build ./cmd/groundtruth-agent                                  # pure Go, no GPU serials, no IAP
CGO_ENABLED=1 go build -tags nvml,iap ./cmd/groundtruth-agent     # what releases ship
```

| Build tag | Adds | Cost |
|---|---|---|
| `nvml` | GPU serial, UUID, VBIOS and driver version through NVML | needs cgo; links dynamically against glibc. `libnvidia-ml.so.1` is **not** shipped: it is loaded with `dlopen` at runtime from the installed NVIDIA driver, and its absence only produces a warning. |
| `iap` | `netbox.auth: iap` | pulls in Google auth libraries |

## Usage

```
groundtruth-agent collect [-config FILE]            print the snapshot as JSON; does not contact NetBox
groundtruth-agent plan    [-config FILE] [-json]    show the changes (dry run)
groundtruth-agent apply   -confirm [-config FILE] [-json]
groundtruth-agent version
```

Run as root: the SMBIOS table and some disk serials are only readable by root.

| Exit code | Meaning |
|---|---|
| 0 | success, nothing to change |
| 1 | error (nothing written if it happened before applying) |
| 2 | changes planned (`plan`) or applied (`apply`) |
| 3 | no device with the observed serial in NetBox |
| 4 | several devices share the observed serial |
| 5 | aborted by `max_changes` / `max_deletes`; nothing written |

Every run ends with one JSON log line on stderr. It has the counts of creates, updates and deletes, warnings, report-only notes (device type or IP mismatches, unregistered NICs), and the exit code.

### Prometheus

With `metrics.textfile` set, every run writes `groundtruth_agent_last_run_timestamp_seconds`, `_success`, `_exit_code`, `_changes{action}` and `_applied` for the node_exporter textfile collector.

### systemd

The package installs `groundtruth-agent.service` (one-shot) and `groundtruth-agent.timer` (5 minutes after boot, then every 6 hours). Neither is enabled automatically.

```sh
sudo systemctl enable --now groundtruth-agent.timer
journalctl -u groundtruth-agent.service
```

The service runs `plan`. The suggested rollout:

1. Run `plan` on a schedule for a while and read the reports.
2. Once the plans look right, switch to `apply` with a drop-in:

```sh
sudo systemctl edit groundtruth-agent.service
# [Service]
# ExecStart=
# ExecStart=/usr/bin/groundtruth-agent apply -confirm -config /etc/groundtruth-agent/config.yaml
```

## Connecting to NetBox

- **Direct** (default): HTTPS to `netbox.url`. Use `netbox.ca_file` to trust a private CA.
- **Extra headers**: `netbox.headers` adds headers to every request, for an authenticating proxy in front of NetBox. Values support `${ENV}` expansion, so secrets can come from the environment (for example `EnvironmentFile=` in a systemd drop-in).
- **Google Cloud IAP** (`netbox.auth: iap`, `iap` build tag): the agent gets a Google-signed ID token for `netbox.iap.audience` (the OAuth client ID of the IAP resource). It uses a service account key (`credentials_file`) or Application Default Credentials (for example the GCE metadata server). The token is sent as `Proxy-Authorization: Bearer …`, which IAP accepts so that the NetBox token in `Authorization` reaches NetBox unchanged ([IAP docs](https://cloud.google.com/iap/docs/authentication-howto)). Programmatic access does not work with a Google-managed OAuth client, and user credentials from `gcloud auth application-default login` are not supported.

The API token comes from `netbox.token_file` or the environment variable named by `netbox.token_env` (default `NETBOX_TOKEN`), never from the configuration file itself.

## Configuration

See [`examples/config.yaml`](examples/config.yaml), which lists every option with its default.

## Libraries

| Library | Used for | Why |
|---|---|---|
| Go standard library | sysfs/procfs, SMBIOS parsing, NetBox REST client, CLI, logging (`log/slog`) | The SMBIOS structures and the few NetBox endpoints are small enough to handle directly. Fewer dependencies to vet, and every input can be replaced by a fixture. |
| [`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml) | configuration | maintained successor of `gopkg.in/yaml.v3` (MIT / Apache-2.0) |
| [`github.com/NVIDIA/go-nvml`](https://github.com/NVIDIA/go-nvml) | GPU serials (`nvml` tag) | NVIDIA's official bindings; loads NVML at runtime (Apache-2.0) |
| [`cloud.google.com/go/auth`](https://pkg.go.dev/cloud.google.com/go/auth) | IAP ID tokens (`iap` tag) | Google's current auth library (Apache-2.0) |

Considered and not used: `github.com/jaypipes/ghw`, a broad hardware library that brings more dependencies than the handful of sysfs files read here. Reading those files directly also keeps the test fixtures simple. `github.com/netbox-community/go-netbox` is generated from a single NetBox release's API schema, while the agent must handle both MAC address models. The licenses of all dependencies are listed in [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).

## Development

```sh
go test ./...                               # fixtures in testdata/rootfs, fake NetBox in internal/netbox/netboxtest
go test ./internal/source/local -update     # accept a changed snapshot
golangci-lint run ./...
```

Integration test against a real, throwaway NetBox started with [netbox-docker](https://github.com/netbox-community/netbox-docker). It needs Docker with access to Docker Hub. It is also available as the manual *Integration* workflow in GitHub Actions:

```sh
./test/integration/run.sh                                         # NetBox version of netbox-docker main
NETBOX_DOCKER_REF=<release> VERSION=<image tag> ./test/integration/run.sh   # e.g. an older NetBox
```

The test imports `examples/custom-fields.yaml` through the API and creates the owner tag and a device for the fixture. It then checks the plan, applies it, and checks that hand-made items stay, stale owned items go, and a second run changes nothing.

`testdata/rootfs` is a hand-made sysfs/procfs tree with a synthetic SMBIOS table. All serial numbers, MAC addresses (from the documentation range `00:00:5E:00:53:00/24`) and host names in it are made up. Keep it that way when adding fixtures.

## Open items

- The license is Apache-2.0 for now; the final decision is pending.
- Contribution sign-off (DCO or CLA) is not decided yet.
- The agent has not been run against a live NetBox yet. The API shapes were checked against the NetBox 4.7 source, and `test/integration` is ready to run, but it hasn't been run so far.
- The custom field file is imported through the REST API in the integration test. Importing it through the UI's bulk import form is untested.
- The minimal permission set in [Required permissions](#required-permissions) has not been tested with a restricted token.

## License

Apache License 2.0, see [LICENSE](LICENSE).
