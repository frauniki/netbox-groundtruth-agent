# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `collect`, `plan`, `apply` and `version` commands.
- Local collection on Linux: system and board serial, BIOS, OS, CPU topology, SMBIOS memory modules, disks, physical NICs, GPUs (NVML with `nvml` build tag, PCI fallback), IP addresses.
- NetBox sync: device lookup by serial, device custom fields, interface MAC addresses (NetBox 4.0–4.1 and 4.2+ models), optional interface creation, tagged inventory items with safe deletion.
- Safety limits on changes and deletions, dry run by default, JSON run reports, node_exporter textfile metrics.
- Direct, extra-header and Google Cloud IAP (`iap` build tag) connections.
- Integration test against a throwaway netbox-docker instance (`test/integration/run.sh`, manual workflow).
- systemd service and timer, GoReleaser configuration for binaries, `.deb` packages, checksums, SBOMs and cosign signatures.
