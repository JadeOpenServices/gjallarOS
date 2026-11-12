# Upgrading older installs

Notes for machines installed with an older gjallarOS. A new install needs
none of this.

## Moving an older install to `/etc/gjallar/system`

Installs made before the system checkout build from a checkout in your home
folder. To move one over, run as your user:

```sh
gjallarctl system move
```

It shows the commit the system runs, and lists local commits and changes in
your checkout that the move leaves behind. Your checkout keeps them; local
`.nix` changes go into the developer-mode folder instead (see
`gjallarctl system apply`). The running commit must be on an official
release channel (`dev`, `staging` or `stable`); a system built with local
changes refuses until you rebuild from a pushed commit. Set `releaseChannel`
first: the new checkout follows that channel. Then, as root:

```sh
sudo gjallarctl system clone --from ~/Documents/gjallarOS
sudo gjallar-system-rebuild
```

`system clone` clones the official repository at the running commit and
copies `generated/` and `user.config.json` from the old checkout as files.
It never runs git in the old checkout. The rebuild switches
`/etc/gjallar/repository` to the new checkout; plain `gjallarctl rebuild`
uses it from then on. The old checkout stays where it is.

When `/etc/gjallar/system` is missing or broken, `gjallarctl rebuild` says
so and names the same `system clone` step.

## Settings that changed

| Old setting | What happens now |
| --- | --- |
| `planeEnable`, `planeHost` | The next rebuild moves them into `webApplications` once and says so |
| draw.io | Removed; the next rebuild drops it once and says so |
| `aiEnable`, `containersEnable`, `nemuEnable` | Refused; list `ai`, `containers` or `nemu` in `apps` instead |

draw.io's browser profile in `~/.config/gjallarOS/chromium-drawio` stays
until you delete it.

Installs made before the subvolume layout get no
[snapshots](../README.md#snapshots) until they are reinstalled.

## Firefox add-ons from older versions

Older versions linked add-ons into the Firefox profile. Close Firefox before
the first `gjallarctl rebuild` after the upgrade. On the next start Firefox
keeps uBlock Origin with its settings, at the version it had. The Stylix
theme comes back on the second start. A rollback never downgrades an
add-on; Firefox refuses that.

## Old names

Every rename lives in one table, `system/tools/legacy/renames.json`:
commands, settings keys, config fields, partition labels, units and
paths. A row holds the old name, the new name, where it was removed, the
release after which it can go (`deleteAfter`, a tag like `v2026.10.70`)
and the Plane or JODS item.

What an old name does until its row goes:

| Old name | What happens |
| --- | --- |
| A command | It does not run; the shell prints the new name and stops |
| A field in `user.config.json` | It is not read; everything that reads the file stops and names the new field |
| A command name that is a real command again | The system does not build |

For example, `rebuild` prints
`gjallarOS: rebuild was removed; use gjallarctl rebuild`, and an old field
stops with "unknown field: OLD is now NEW; move the value there in
user.config.json".

The flake check `legacy-names` fails while an old name is still used
outside `system/tools/legacy/`, and once the release in `system/release`
is newer than a row's `deleteAfter` tag. Then the row and the last traces
are removed. A revision gives the same answer on every machine and day.

A rename adds its row in the same commit. Tests and captured device
output may still name the old name.
