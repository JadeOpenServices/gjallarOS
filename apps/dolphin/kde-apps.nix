# The KDE apps that come with Dolphin. pkgs/overlays/kio.nix patches KIO,
# so none of them is on cache.nixos.org: flake.nix builds this list into
# `prebuilt` for the release cache. A KDE app added here is cached too.
pkgs: with pkgs.kdePackages; [
  dolphin-plugins
  ark
  gwenview
  kio-extras
  kio-fuse
  kdegraphics-thumbnailers
  ffmpegthumbs
  konsole
  kfind
  filelight
]
