# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue. Report vulnerabilities privately through GitHub's [private vulnerability reporting](https://github.com/frauniki/netbox-groundtruth-agent/security/advisories/new).

Include the affected version, a description of the issue and, if possible, steps to reproduce. You should receive an acknowledgement within a week. Fixes are released as a new version, and the advisory is published once users can upgrade.

## Supported versions

Only the latest release receives security fixes.

## Scope notes

The agent runs as root and holds a NetBox API token that can modify devices. Issues of particular interest include anything that could leak the token, write to NetBox objects outside the documented scope, delete data the agent does not own, or be triggered by untrusted input from the machine (for example crafted SMBIOS strings or sysfs contents).
