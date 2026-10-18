# Fixes for other people's code

Each folder here fixes one bug in a package or NixOS module we do not own.
Keeping them in one place shows what we carry and makes each one easy to
drop when upstream fixes the bug.

## Rules

- One folder per fix, named after what it fixes (`avahi-daemon/`). It holds a
  `default.nix` NixOS module and any patches.
- The module only acts when the thing it fixes is on (for example
  `lib.mkIf config.services.avahi.enable`).
- The module that uses the fixed thing imports the fix, with a comment that
  points here. Search for `fixes/<name>` to find the link.
- The header of `default.nix` says:
  - what goes wrong, with the log line or symptom and the Plane issue,
  - why it goes wrong, down to the upstream file or option,
  - what the fix does and what it costs,
  - the upstream bug or the report waiting in the human-session queue,
  - the nixpkgs and package version it was checked against,
  - when to remove it.
- A patch to a package asserts the version it was written for, so an update
  stops the build until someone checks the fix again.
- A test in `tests/nix/` runs the bug on a stock machine next to a fixed one.
  When the stock machine stops failing, the test fails and says which part
  to remove.

## Current fixes

| Fix | Linked from | Plane | Remove when |
| --- | --- | --- | --- |
| `avahi-daemon/` | `system/services/printing.nix` (ipp-usb turns avahi on) | 181 | the test's stock machine survives a stale pid file; avahi is newer than 0.8 and handles SIGHUP before writing its pid file |
| `systemd-partition-reread/` | `system/services/removable-media.nix` (sticks written with dd) | 185 | the test's stock machine shows every new partition and the new disk label after a dd; systemd is newer than 260.2 and removes partitions in the way before adding |
