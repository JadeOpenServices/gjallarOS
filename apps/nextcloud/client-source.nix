# Pinned Nextcloud desktop source and the GjallarOS patches on top of it.
# Shared by apps/nextcloud/package.nix and tests/nix/nextcloud-client-patches.nix.
{ fetchFromGitHub, replaceVars }:
let
  dolphinNextcloudIcons = import ../../themes/apps/dolphin/nextcloud-icons.nix;
in
{
  src = fetchFromGitHub {
    owner = "nextcloud";
    repo = "desktop";
    rev = "d1f9f6469a038ca223e1e86ad1dcc5c199735957";
    sha256 = "14mzqq4133m6sj9sm9ari0x53n1dsyjdsidmpyz3yyzl231vn7jm";
  };

  # The nixpkgs patch targets the old 4.0.8 source. GjallarOS already
  # disables upstream autostart explicitly, so it is not carried over.
  patches = [
    # Every concurrent FUSE request must receive a result from the shared download.
    ./patches/nextcloud-hydration-liveness.patch
    # Dehydrate files hydrated on demand below an OnlineOnly folder again.
    ./patches/nextcloud-openvfs-effective-pin.patch
    # Skip the no-op VFS metadata job for every unchanged file on each sync.
    ./patches/nextcloud-openvfs-in-sync.patch
    # No sync loop from conflict-file pins; no re-upload after hydration.
    ./patches/nextcloud-openvfs-sync-loop.patch
    # A folder syncs only once the OpenVFS mount is confirmed; a failed
    # start is a setup error, not a sync on the bare directory.
    ./patches/nextcloud-openvfs-start-gate.patch
    # A mount left from an earlier start is a setup error, not a started
    # VFS, and the client does not spawn a second openvfs onto it.
    ./patches/nextcloud-openvfs-stale-mount.patch
    # A folder answers for the files below it, so the file manager shows
    # the cloud until every file is local.
    ./patches/nextcloud-openvfs-folder-availability.patch
    # A folder's keep or free-up choice covers the folders below it, also
    # those that had their own choice.
    ./patches/nextcloud-openvfs-folder-pin-below.patch
    # New accounts start as virtual-files folders on Linux too, unless the
    # server's managed settings turn virtual files off.
    ./patches/nextcloud-vfs-default.patch
    # Windows, menus and popups follow the Qt palette (Noctalia via qt6ct,
    # else the system palette) instead of hardcoded light/dark colors.
    ./patches/nextcloud-palette-theme.patch
    # A rebuilt client never adds itself to autostart; GjallarOS starts it.
    ./patches/nextcloud-no-autostart.patch
    # A folder added in the settings follows the VFS default too, not only
    # the wizard checkbox. A server that enforces VFS off still wins.
    ./patches/nextcloud-vfs-folder-wizard.patch
    # NEXTCLOUD_BROWSER_OPENER gets web links (login flow included) and
    # NEXTCLOUD_BROWSER_CLOSER runs after login. Mail links stay with the
    # desktop default.
    ./patches/nextcloud-browser-hooks.patch
    # The socket reports OpenVFS availability, Dolphin shows it as emblems.
    (replaceVars ./patches/nextcloud-dolphin-vfs-overlays.patch {
      onlineOnlyIcon = dolphinNextcloudIcons.onlineOnly;
      localIcon = dolphinNextcloudIcons.local;
    })
    # After the wizard, pick folders to keep on this device; everything
    # else, new top-level folders too, stays online-only.
    ./patches/nextcloud-keep-local-picker.patch
    # A bad option from a second start is logged and ignored; it no longer
    # exits the running client.
    ./patches/nextcloud-remote-options-no-exit.patch
    # The folder list is titled "Sync folders", not "Classic sync".
    ./patches/nextcloud-sync-folders-title.patch
    # Hydration counts the bytes it wrote. The Content-Length header is gone
    # or counts compressed bytes when the server gzips a reply.
    ./patches/nextcloud-hydration-encoded-size.patch
    # A failed or running hydration is never uploaded as a local edit.
    ./patches/nextcloud-openvfs-failed-hydration.patch
    # A click on a client notification opens the tray window, like a click
    # on the tray icon (Plane 189).
    ./patches/nextcloud-notification-click.patch
    # An opened file stays on disk; "Free up space" empties it at any time;
    # it goes back to online-only once nothing has read it for 14 days. A
    # file a program has open is never emptied; openvfs tells the client
    # when the last program closes a file freed up meanwhile (Q79, Plane 254).
    ./patches/nextcloud-openvfs-keep-open-files.patch
    # SIGTERM quits as from the tray: folders stop and openvfs unmounts
    # before the client exits (Plane 184).
    ./patches/nextcloud-quit-on-sigterm.patch
  ];
}
