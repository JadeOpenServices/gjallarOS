# Nextcloud/OpenVFS hydration patches

These patches apply to the source revisions pinned in `../package.nix` and
`../client-source.nix`. Every source change is a patch file here; the build
only checks that upstream still has the options GjallarOS relies on.

Client behaviour patches:

- `nextcloud-no-autostart.patch`: the client never adds itself to autostart,
  neither after the wizard nor on first start (the "launch on startup"
  setting now defaults to off). The session starts it.
- `nextcloud-vfs-folder-wizard.patch`: a folder added in the settings
  becomes a virtual-files folder when VFS is the default, not only when the
  wizard checkbox is set.
- `nextcloud-browser-hooks.patch`: web links go to `NEXTCLOUD_BROWSER_OPENER`
  (the login window for login flow URLs) and `NEXTCLOUD_BROWSER_CLOSER` runs
  after a successful login. Mail links keep the desktop default.
- `nextcloud-dolphin-vfs-overlays.patch`: the socket appends the OpenVFS
  availability to file status and the Dolphin plugin maps it to emblems.
  `@onlineOnlyIcon@` and `@localIcon@` come from
  `themes/apps/dolphin/nextcloud-icons.nix` through `replaceVars`.
- `nextcloud-openvfs-stale-mount.patch`: an OpenVFS mount that is already
  there before the client starts its own openvfs is a setup error. The start
  gate took such a mount for its own, and the second openvfs's exit aborted
  the client.
- `nextcloud-openvfs-folder-availability.patch`: a folder reports what the
  sync database knows about the files below it, so the file manager shows
  the cloud until every file is local.
- `nextcloud-openvfs-folder-pin-below.patch`: "Always available locally" or
  "Free up space" on a folder resets the pins of everything below it to
  inherited, as the other VFS backends do. Before, a
  subfolder kept its own earlier pin on disk and stayed downloaded.
  Excluded pins follow the ignore list and stay.
- `nextcloud-keep-local-picker.patch`: after the wizard a virtual-files
  account opens "Keep on this device" with nothing ticked. Ticked folders get
  the "always available locally" pin; closing or "Not now" leaves everything
  online-only. The picks live only in this device's sync database (list
  type 100) until each folder exists and carries its pin, also across an
  interrupted first sync. A pick the next full sync does not find is
  dropped. Logs show counts, never folder names. The build enforces
  virtual files, which upstream also takes to mean hiding "Make always
  available locally"; the patch shows it again in Dolphin and in the folder
  settings, so folders can be picked later. Turning virtual files off stays
  locked.
- `nextcloud-remote-options-no-exit.patch`: a second `nextcloud` start
  forwards its options to the running client. Upstream checked them with the
  code meant for a fresh start, so a typo or unknown option exited the
  running client: sync stopped and the OpenVFS mount went away. The running
  client now logs a warning and does not act on that command.
- `nextcloud-sync-folders-title.patch`: upstream titles the folder list
  "Classic sync" to set it apart from macOS File Provider. Linux has no File
  Provider, so the title sat above virtual-file folders and said the
  opposite of what they do. It now reads "Sync folders".

Hydration patches:

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
  cancellation, or a five-minute deadline. A failed download fails the open
  with EIO. Upstream returned ENOENT, so a program saw the file as gone and
  could save a new file over the placeholder.
- The five-minute deadline counts from the last write into the file, not from
  the request. The sync client writes the download through the mount, so a
  large file on a slow link keeps its reader waiting while data arrives.
  Upstream failed every open that took longer than five minutes, even with
  the download still running.
- OpenVFS ends a hydration wait when the FUSE session exits. libfuse joins
  every worker before it unmounts and a worker cannot be cancelled during a
  request, so upstream ignored SIGTERM for up to five minutes. systemd then
  killed openvfs together with its unmount helper and left a dead mount.
- OpenVFS notices when the sync client closes its socket. Pending hydrations
  fail at once and new ones fail without waiting for the deadline. Upstream
  read EOF as "no data" and kept the closed socket's fd number for later
  writes.
- OpenVFS unmounts when the sync client closes its socket, which it only does
  when it exits, and does not mount when it cannot reach the client. Upstream
  kept serving a mount after the client was killed; the restarted client's
  openvfs could not mount over it and the client aborted.
- OpenVFS keeps FUSE workers free for the sync client. libfuse serves the
  mount from one pool of 10 workers by default, and a hydration wait holds
  its worker until the client has written the file through the same mount.
  Ten apps opening online-only files at once took every worker, so the
  client's own writes, and its sync database under `~/Nextcloud`, got none
  and everything touching the mount hung, terminals included. openvfs now
  runs 64 workers; hydration waits may hold 48 of them, at most 16 per
  process, and an open past that fails at once with `EAGAIN` instead of
  waiting.
- OpenVFS writes the messages it exchanges with the sync client only to the
  debug log (`-d`). Upstream printed every hydration, with the file path and
  the app that opened it, to the journal on every run. Errors are still
  logged.
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
- Hydration checks the bytes it wrote against the server's size. Upstream read
  the Content-Length header, which Qt drops when it unpacks a gzip reply over
  HTTP/1.1, so every file the server compressed (JSON, text) failed after a
  complete download. Over HTTP/2 Qt keeps the compressed length, so the
  download no longer compares it for encoded replies either.
- A failed hydration empties the file and restores the dehydrated placeholder
  with the server's size and mtime, and a file still marked Hydrating counts as
  virtual during discovery. Upstream left the partial content, the Hydrating
  state and the download time as mtime, so the next sync uploaded it as a local
  edit and the reader retried, in a loop (Plane 254).

The integration fixture uses a fake sync socket and a fresh temporary FUSE
mount, never the user's Nextcloud directory:

```sh
python3 tests/openvfs-hydration.py /path/to/openvfs --timeout
```

It checks concurrent opens, in-progress hydration, wrapped preview exclusion,
server errors, cancellation with zero blocked requests, SIGTERM and sync
client exit during a hydration (both must unmount), no mount without a sync
client, the actual deadline, a slow download that outlasts it, and a burst
of 60 opens that leaves other processes and the client's own writes served.
It does not replace an integration check against the rebuilt Nextcloud client.

KIO 6.26 normally runs its file worker inside Dolphin. `dolphin.nix` uses
`KIO_ENABLE_WORKER_THREADS=0` for Dolphin and its D-Bus launcher so OpenVFS can
distinguish intentional file transfers from preview reads. Both `file.so` and
`kio_file.so` plugin names are recognized.

KIO 6.26 also takes the OpenVFS mount for a fast local disk, so Dolphin reads
the start of every file to guess its type and asks for thumbnails, and each
read downloads an online-only file (Plane 252). `pkgs/overlays/kio.nix` adds the
KIO 6.31 change that counts `fuse.*` mounts not backed by a `/dev/` device as
slow: types come from the file name only and the remote preview limits apply.
It also adds upstream 26081ad6: the file worker stops reading files on slow
mounts to find their type. Dolphin asks the worker for files whose name gives
no type (no extension, `.bin`), and without it those were still downloaded.
`checks.kio-fuse-slow` tests both on a bindfs mount and says when stock KIO no
longer needs the overlay.

Build `tests/nix/kio-copy.nix` with the host's `pkgs` and pass its `bin/dolphin`
to the fixture as `--kio-copy=/path/to/bin/dolphin` to exercise actual KIO. The
fixture verifies the threaded failure and the separate-worker fix, without a
Nextcloud account or changes to real synced files.
