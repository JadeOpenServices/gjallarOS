# Pinned Nextcloud desktop source and the GjallarOS patches on top of it.
# Shared by apps/nextcloud/package.nix and tests/nix/nextcloud-client-patches.nix.
{ fetchFromGitHub }:
{
  src = fetchFromGitHub {
    owner = "nextcloud";
    repo = "desktop";
    rev = "fc07fe7474afb938dd5253aded6da0ea1f664704";
    sha256 = "0q387p0g3n13d8rw5rp6l6y48i4vmadr843a9b8s1xzhdp9wvxqw";
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
    # Keep the Fusion QML style; qt6ct's KDE patch breaks the account wizard.
    ./patches/nextcloud-qml-style.patch
    # New accounts start as virtual-files folders on Linux too.
    ./patches/nextcloud-vfs-default.patch
    # Windows, menus and popups follow the Qt palette (Noctalia via qt6ct,
    # else the system palette) instead of hardcoded light/dark colors.
    ./patches/nextcloud-palette-theme.patch
  ];
}
