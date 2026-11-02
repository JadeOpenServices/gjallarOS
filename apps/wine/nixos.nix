# NixOS side of the Wine app.
{ config, lib, ... }:

{
  # Wine 11's wineserver uses the kernel's ntsync driver for thread sync
  # when /dev/ntsync is there, and the sandbox passes the node in
  # (internal/winesandbox). The module has no device alias, so nothing
  # loads it on demand. The kernel opens the node to every local user
  # (mode 0666); each open is its own instance without names, as in
  # Flatpak 1.18, which passes it to every app. Plane 277 CP8c, Q194.
  config = lib.mkIf config.gjallar.apps.wine.enable {
    boot.kernelModules = [ "ntsync" ];
  };
}
