# Maintainers

Notes for people who work on gjallarOS itself: trying changes and the
release cache.

## Trying a gjallarctl change

To try a `gjallarctl` change before rebuilding, run it from the checkout with
`go run ./cmd/gjallarctl rebuild` (or any other command). It uses the sudo
helpers of the installed system, so this works only on a machine that already
runs gjallarOS. If you need the package itself, build it with
`nix build "git+file://$PWD#gjallarctl"`. A `path:` flake reference copies the
whole directory into the Nix store, untracked files and VM disks included.

## Release cache

gjallarOS patches a few packages (Noctalia, its greeter, the Nextcloud client
with OpenVFS, OpenCode, Hyprland, Ghostty, Dolphin and the KDE apps that
come with it, and others), so cache.nixos.org does not have them and every
machine compiled them itself. For each `v*` release tag,
`.github/workflows/cache.yml` builds them (flake package `prebuilt`), signs
them and uploads them as files of the `nix-cache` release. Those files are a
plain Nix binary cache. Each tag adds to it, so a machine on any channel
downloads a package whenever it matches one built for any tag. Anything
missing still builds locally. The workflow itself downloads from the same
cache, so a tag builds only the packages that changed since the tags before
it.

`system/release` holds the name of the last release tag. The commit that gets
a new tag sets it to that tag first; the workflow stops when the two differ.
Commits after a tag keep the last release name until the next one.

`deployment/binary-cache.json` names the cache and its public key:

```json
{
  "url": "https://github.com/JadeOpenServices/gjallarOS/releases/download/nix-cache",
  "publicKey": "gjallaros-cache-1:..."
}
```

Machines add it to `nix.conf`, and `gjallarctl rebuild` and the installer
pass it on the command line too, so the first install uses it as well. Nix
only accepts files signed with that key.

The workflow reads the signing key (secret `NIX_SIGNING_KEY`) only through the
`release-cache` environment, which should allow `v*` tags only and require a
reviewer. Create the key on your machine; it never needs network:

```sh
nix key generate-secret --key-name gjallaros-cache-1 > cache.sec
nix key convert-secret-to-public < cache.sec
```

Paste `cache.sec` into the secret, put the public key into
`deployment/binary-cache.json`, then delete `cache.sec` or keep it offline.
The key signs cache files and nothing else. Forks keep
`deployment/binary-cache.json` empty or point it at their own cache.
