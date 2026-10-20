# GjallarOS local changes

Developer mode builds the files in this folder into your system.

- `system/` holds NixOS modules for the whole system.
- `home/` holds home-manager modules for your account.

Every `.nix` file below `system/` and `home/` is built. A file or folder
whose name starts with `_` is skipped, like the two examples here. Rename
one without the `_` to build it.

The folder may hold this README and, below `system/` and `home/`, files
ending in .nix .json .toml .yaml .yml .conf .txt .md .png .jpg .jpeg .svg.
Names use letters, digits and `. _ + -`; nothing hidden, at most 4 levels,
256 files, 1 MiB per file and 4 MiB in all.

Nothing here is built until you run

    gjallarctl system apply

It shows every change against the system's copy and asks for your
password. `gjallarctl system status` shows what the running system was
built with.

Code in this folder runs as root. If a change breaks the system, pick the
previous generation in the boot menu. Turning developer mode off keeps
this folder.
