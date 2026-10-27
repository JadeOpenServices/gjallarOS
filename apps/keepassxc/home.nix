{ config, lib, pkgs, ... }:
lib.mkIf config.gjallar.apps.keepassxc.enable {
  home.packages = [
    pkgs.keepassxc
  ];

  # KeePassXC writes its own store path into the browser manifests, and a
  # garbage collection later removes it. Point them at the user profile.
  home.activation.keepassxcStableProxy = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    proxy=${lib.escapeShellArg "${config.home.profileDirectory}/bin/keepassxc-proxy"}
    for manifest in \
      "$HOME/.mozilla/native-messaging-hosts/org.keepassxc.keepassxc_browser.json" \
      "$HOME/.librewolf/native-messaging-hosts/org.keepassxc.keepassxc_browser.json"; do
      if [ -f "$manifest" ] && [ ! -L "$manifest" ] \
        && ${pkgs.gnugrep}/bin/grep -q -E '"path": "/nix/store/[^"]+/bin/keepassxc-proxy"' "$manifest"; then
        run ${pkgs.gnused}/bin/sed -i -E \
          "s|\"path\": \"/nix/store/[^\"]+/bin/keepassxc-proxy\"|\"path\": \"$proxy\"|" \
          "$manifest"
      fi
    done
  '';

  xdg.mimeApps = {
    enable = true;

    defaultApplications = {
      "application/x-keepass2" = "org.keepassxc.KeePassXC.desktop";
    };
  };
}
