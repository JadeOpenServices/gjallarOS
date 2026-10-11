# Nextcloud/OpenVFS hydration patches

These patches apply to the source revisions pinned in `../package.nix` and
`../client-source.nix`.

- Nextcloud shares an existing hydration job with every requesting socket job,
  starts its download only once, and returns an error when setup fails.
- Nextcloud's OpenVFS backend decides hydration and dehydration from the
  effective (inherited) pin state, like the xattr and suffix backends. A file
  opened or copied out of an OnlineOnly folder is downloaded for that read and
  becomes online-only again on the next sync run, if unchanged. Pin a file or
  folder "always available locally" to keep it. Files without placeholder
  attributes (new local files) are never dehydrated.
- OpenVFS identifies the sync client from Unix socket peer credentials because
  Nextcloud does not include a PID in its VERSION response. Client threads must
  be able to write downloaded content without requesting hydration themselves.
- OpenVFS registers requests before sending them, uses atomic request IDs,
  waits for files already being hydrated, and releases waiters on errors,
  cancellation, or a five-minute deadline.
- OpenVFS placeholders always report themselves in sync. Upstream marked every
  unchanged file for a metadata update on each sync run, but OpenVFS cannot
  record that it finished, so tens of thousands of no-op jobs repeated forever.
  Missing placeholder attributes are still repaired by the separate metadata
  check.
- Hydration closes the downloaded file before restoring the server mtime.
  Upstream closed it later, so the final buffered write bumped the mtime and
  every file opened from an online-only folder was uploaded again.
- OpenVFS skips pin writes that change nothing and never marks conflict files
  Excluded. Every xattr write wakes the folder watcher, which resets a conflict
  file to Inherited and schedules a sync that marks it Excluded again: the
  client synced every few seconds and other uploads looked stuck.
- Ignore-app suffix matching recognizes Nix's `-wrapped` executables. The
  upstream ignore list therefore also applies to wrapped previews. KIO file
  protocol workers are exempt from the generic suffix rule so intentional copies
  can hydrate; explicit full-path exclusions still take precedence.

The integration fixture uses a fake sync socket and a fresh temporary FUSE
mount, never the user's Nextcloud directory:

```sh
python3 tests/openvfs-hydration.py /path/to/openvfs --timeout
```

It checks concurrent opens, in-progress hydration, wrapped preview exclusion,
server errors, cancellation with zero blocked requests, and the actual deadline.
It does not replace an integration check against the rebuilt Nextcloud client.

KIO 6.26 normally runs its file worker inside Dolphin. `dolphin.nix` uses
`KIO_ENABLE_WORKER_THREADS=0` for Dolphin and its D-Bus launcher so OpenVFS can
distinguish intentional file transfers from preview reads. Both `file.so` and
`kio_file.so` plugin names are recognized.

Build `tests/nix/kio-copy.nix` with the host's `pkgs` and pass its `bin/dolphin`
to the fixture as `--kio-copy=/path/to/bin/dolphin` to exercise actual KIO. The
fixture verifies the threaded failure and the separate-worker fix, without a
Nextcloud account or changes to real synced files.
