{
  config,
  osConfig ? { },
  pkgs,
  lib,
  ...
}:
let
  # Hyprland matches a client by the full path of its executable, so every
  # rule covers the whole store path of one package. Store paths are
  # read-only, so a user program cannot pose as one of these.
  inPackage = pkg: "${lib.escapeRegex (toString pkg)}/.*";
  portal = osConfig.programs.hyprland.portalPackage or pkgs.xdg-desktop-portal-hyprland;
  pluginPath = plugin: "${plugin}/lib/lib${plugin.pname}.so";

  rules = [
    "${inPackage portal}, screencopy, allow"
    "${inPackage portal}, cursorpos, allow"
    "${inPackage config.programs.hyprlock.package}, screencopy, allow"
  ]
  ++ lib.optionals config.programs.noctalia.enable [
    "${inPackage config.programs.noctalia.package}, screencopy, allow"
  ]
  ++ map (plugin: "${lib.escapeRegex (pluginPath plugin)}, plugin, allow") (
    builtins.filter lib.isDerivation config.wayland.windowManager.hyprland.plugins
  );

  # Hyprland keeps the rules it started with until the next login, but reads
  # enforce_permissions again on every config reload. It records which rules
  # it started with, and activation compares that with the new set: a Noctalia
  # or hyprlock update moves their store paths, and until a new login every
  # screenshot of theirs would ask for permission. A running session without a
  # record started before these rules existed and now enforces with none.
  rulesId = builtins.hashString "sha256" (builtins.toJSON rules);
  startedRules = "gjallar/hyprland-permissions";
  recordRules = pkgs.writeShellScript "gjallar-record-permissions" ''
    set -eu
    mkdir -p "$XDG_RUNTIME_DIR/gjallar"
    echo ${rulesId} >"$XDG_RUNTIME_DIR/${startedRules}"
  '';
in
{
  # Without this any program in the session can read the screen through
  # wlr-screencopy without the user knowing. Clients without a rule get a
  # hyprland-dialog prompt; screen sharing for browsers and calls goes
  # through the portal and its own window picker.
  # https://wiki.hypr.land/Configuring/Advanced-and-Cool/Permissions/
  # Rules load only at Hyprland start, so a change needs a new login.
  wayland.windowManager.hyprland.settings = {
    ecosystem.enforce_permissions = true;
    permission = rules;
    exec-once = [ "${recordRules}" ];
  };

  # After reloadSystemd: a Noctalia update restarts Noctalia, which is also the
  # notification server, and a notice sent before that vanishes with it.
  home.activation.gjallarPermissionsNotice = lib.hm.dag.entryAfter [ "reloadSystemd" ] ''
    runtime=/run/user/$(${pkgs.coreutils}/bin/id -u)
    gjallar_started_rules=$(${pkgs.coreutils}/bin/cat "$runtime/${startedRules}" 2>/dev/null || true)
    gjallar_hyprland=$(XDG_RUNTIME_DIR=$runtime ${pkgs.hyprland}/bin/hyprctl instances -j 2>/dev/null || true)
    if [ "$gjallar_started_rules" != ${rulesId} ] && [[ "$gjallar_hyprland" == *'"pid"'* ]]; then
      DBUS_SESSION_BUS_ADDRESS=unix:path=$runtime/bus \
        ${pkgs.glib.bin}/bin/gdbus wait --session --timeout 15 org.freedesktop.Notifications ||
        true
      run ${pkgs.coreutils}/bin/env DBUS_SESSION_BUS_ADDRESS=unix:path=$runtime/bus \
        ${pkgs.libnotify}/bin/notify-send \
        --app-name=GjallarOS --icon=system-log-out \
        "Log out to finish the update" \
        "Screen capture rules changed. Until you log in again, screenshots may ask for permission." ||
        true
    fi
  '';
}
