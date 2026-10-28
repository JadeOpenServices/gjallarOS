{
  config,
  inputs,
  lib,
  pkgs,
  settings,
  ...
}:
let
  greeterHelper = "${config.programs.noctalia-greeter.package}/bin/noctalia-greeter-apply-appearance";

  greeterUser = config.services.greetd.settings.default_session.user;
  greeterGroup = config.users.users.${greeterUser}.group;
  greeterReadyDir = "/run/noctalia-greeter";
  greeterReady = "${greeterReadyDir}/display-ready";

  plymouth = "${config.boot.plymouth.package}/bin/plymouth";
  # Returns once plymouthd has exited; the greeter keeps the card open, so the
  # screen stays on its frame.
  quitPlymouth = pkgs.writeShellScript "plymouth-quit-greeter" ''
    ${plymouth} --ping || exit 0
    exec ${plymouth} quit --retain-splash --wait
  '';

  # On-screen keyboard at the login screen of touchscreen machines. The
  # greeter compositor starts it with --hidden and shows it on touch.
  # --no-popup: no enlarged preview of the pressed key, which would show the
  # password to anyone watching.
  greeterOsk = pkgs.writeShellScript "gjallar-greeter-osk" ''
    exec ${pkgs.wvkbd}/bin/wvkbd-mobintl -H 320 -L 240 --no-popup "$@"
  '';

  # Key for the look in a greeter sync staging directory. Noctalia releases
  # encode the same look differently (header comments, where the wallpaper is
  # installed, rows the greeter never reads), so the key is built from what
  # the greeter uses, not from the raw bytes.
  greeterSyncKey = pkgs.writeText "gjallar-greeter-sync-key.py" ''
    import hashlib
    import json
    import os
    import re
    import sys
    import tomllib

    staging = sys.argv[1]
    colour = re.compile(r"#[0-9A-Fa-f]{6}([0-9A-Fa-f]{2})?")


    def norm(value):
        if isinstance(value, bool):
            return value
        if isinstance(value, (int, float)):
            return float(value)
        if isinstance(value, str) and colour.fullmatch(value):
            return value.lower()
        if isinstance(value, dict):
            return {k: norm(v) for k, v in value.items()}
        if isinstance(value, list):
            return [norm(v) for v in value]
        return value


    def digest(path):
        with open(path, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()


    def sync_toml(path):
        try:
            with open(path, "rb") as f:
                doc = tomllib.load(f)
        except (OSError, ValueError):
            return digest(path)
        appearance = doc.get("appearance", {})
        # The wallpaper path only says where the helper installs the staged
        # wallpaper file; its content is keyed below.
        wallpapers = [appearance.get("wallpaper")]
        wallpapers += list(appearance.get("wallpapers", {}).values())
        for wallpaper in wallpapers:
            if isinstance(wallpaper, dict):
                wallpaper.pop("path", None)
        # The greeter has no reboot_uefi action; Noctalia 5.4 stopped staging
        # the row.
        actions = doc.get("session", {}).get("actions")
        if isinstance(actions, list):
            doc["session"]["actions"] = [
                a for a in actions
                if not (isinstance(a, dict) and a.get("action") == "reboot_uefi")
            ]
        return norm(doc)


    key = {}
    for name in sorted(os.listdir(staging)):
        path = os.path.join(staging, name)
        if not os.path.isfile(path):
            continue
        if name == "sync.toml":
            key[name] = sync_toml(path)
        elif name.startswith("output_"):
            with open(path, encoding="utf-8", errors="replace") as f:
                key[name] = sorted(line.strip() for line in f if line.strip())
        elif name.startswith("wallpaper"):
            # wallpaper.jpg, wallpaper-DP-2.webp: the output, not the format.
            rest = name[len("wallpaper"):]
            rest = "" if rest.startswith(".") else os.path.splitext(rest)[0]
            key["wallpaper" + rest] = digest(path)
        else:
            key[name] = digest(path)
    print(hashlib.sha256(json.dumps(key, sort_keys=True).encode()).hexdigest())
  '';

  # Noctalia re-stages the greeter look on every login and theme load, and
  # each sync is a pkexec password prompt. Ask only when the look differs
  # from the last answered sync. No passwordless polkit rule: the greeter
  # look is shared by every account on the machine.
  greeterSyncPrivilege = pkgs.writeShellScriptBin "gjallar-greeter-sync-privilege" ''
    set -eu
    PATH=${lib.makeBinPath [ pkgs.coreutils ]}:$PATH
    staging="$2"
    dir="''${XDG_STATE_HOME:-$HOME/.local/state}/gjallar"
    state="$dir/greeter-sync.key"
    key=$(${pkgs.python3}/bin/python3 -I ${greeterSyncKey} "$staging")
    if [ -r "$state" ] && [ "$(cat "$state")" = "$key" ]; then
      exit 0
    fi
    mkdir -p "$dir"
    # First sync of a new account is the default look the greeter already
    # shows: record it, do not ask during the welcome flow. An account with
    # only the old raw-bytes record (greeter-sync.sha256) answered every look
    # it staged, so its first sync after the update is recorded the same way.
    if [ ! -e "$state" ]; then
      printf '%s\n' "$key" > "$state"
      rm -f "$dir/greeter-sync.sha256"
      exit 0
    fi
    # Run only the store path the org.gjallaros.greeter-sync action names;
    # the generic pkexec action is refused (local-hardening.nix).
    case "$1" in
      noctalia-greeter-apply-appearance | */noctalia-greeter-apply-appearance) shift ;;
      *) echo "greeter sync: unexpected helper $1" >&2; exit 1 ;;
    esac
    # A dismissed prompt counts as the answer for this look; the next change
    # asks again instead of re-prompting for the same look.
    rc=0
    /run/wrappers/bin/pkexec ${greeterHelper} "$@" || rc=$?
    if [ "$rc" -eq 0 ] || [ "$rc" -eq 126 ]; then
      printf '%s\n' "$key" > "$state"
    fi
    exit "$rc"
  '';

  # The greeter sync helper runs as root through pkexec. Its own action, with
  # the exact store path, replaces the generic pkexec action that
  # local-hardening.nix refuses; auth_admin without _keep asks every time.
  greeterSyncPolicy = pkgs.writeTextDir "share/polkit-1/actions/org.gjallaros.greeter-sync.policy" ''
    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE policyconfig PUBLIC "-//freedesktop//DTD PolicyKit Policy Configuration 1.0//EN"
      "http://www.freedesktop.org/standards/PolicyKit/1/policyconfig.dtd">
    <policyconfig>
      <action id="org.gjallaros.greeter-sync">
        <description>Apply your look to the login screen</description>
        <message>Authentication is required to apply your theme, wallpaper and display layout to the login screen</message>
        <defaults>
          <allow_any>no</allow_any>
          <allow_inactive>no</allow_inactive>
          <allow_active>auth_admin</allow_active>
        </defaults>
        <annotate key="org.freedesktop.policykit.exec.path">${greeterHelper}</annotate>
      </action>
    </policyconfig>
  '';
in
{
  environment.systemPackages = with pkgs; [
    wayland
    wl-clipboard
    bibata-cursors
    greeterSyncPolicy
    greeterSyncPrivilege
  ];

  services.xserver = {
    enable = true;

    xkb = {
      variant = settings.keyboardVariant;
      layout = settings.keyboardLayout;
      options = "grp:win_space_toggle";
    };
  };

  programs.noctalia-greeter = {
    enable = true;
    package = pkgs.callPackage ../../../pkgs/noctalia-greeter {
      noctalia-greeter = inputs.noctalia-greeter.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };

    settings = {
      appearance = {
        scheme = "Synced";
        theme_mode = "dark";
        corner_radius_scale = 2.0;
        font_family = settings.themeDetails.font;
        password_style = "random";
        hide_logo = false;
      };

      session.default = "GjallarOS Hyprland";

      cursor = {
        theme = "Bibata-Modern-Classic";
        size = 24;
        path = "${pkgs.bibata-cursors}/share/icons";
      };

      keyboard = {
        layout = settings.keyboardLayout;
        variant = settings.keyboardVariant;
        options = "grp:win_space_toggle";
      };
    };
  };

  # Plymouth hands the screen to the greeter in two steps, the way GDM does
  # it. greetd deactivates it first: the quit function sets the last frame
  # (themes/lib/boot-splash.script), and plymouthd gives up DRM master but
  # keeps that frame on screen. Once the greeter has drawn a frame of its own
  # (display-ready.patch), plymouthd quits. A plymouthd that exits while no
  # other program has the card open hands the screen back to the kernel
  # console, which then showed the frame from the middle of a spin (Q77).
  services.greetd.greeterManagesPlymouth = true;
  systemd.services.plymouth-quit.enable = false;
  systemd.services.plymouth-quit-wait.enable = false;

  # The greeter can only create this file; root starts a fixed unit when it
  # shows up and never reads it.
  systemd.tmpfiles.rules = [ "d ${greeterReadyDir} 0700 ${greeterUser} ${greeterGroup} -" ];

  systemd.services.greetd.serviceConfig = {
    ExecStartPre = pkgs.writeShellScript "greetd-deactivate-plymouth" ''
      ${pkgs.coreutils}/bin/rm -f ${greeterReady}
      # No daemon: the splash is off or already gone, nothing to hand over.
      ${plymouth} --ping || exit 0
      if ! ${plymouth} deactivate; then
        echo "greetd-deactivate-plymouth: plymouth deactivate failed, starting the greeter anyway" >&2
      fi
    '';
    # A greeter that never drew leaves plymouthd waiting; the shutdown splash
    # starts its own.
    ExecStopPost = quitPlymouth;
  };

  systemd.paths.plymouth-quit-greeter = {
    description = "Wait for the greeter to drive a screen";
    wantedBy = [ "greetd.service" ];
    pathConfig.PathExists = greeterReady;
  };

  systemd.services.plymouth-quit-greeter = {
    description = "Hand the screen from Plymouth to the greeter";
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = quitPlymouth;
    };
  };

  services.greetd.settings.default_session.command = lib.mkForce (
    "${pkgs.coreutils}/bin/env NOCTALIA_GREETER_DISPLAY_READY=${greeterReady} "
    + lib.optionalString (settings.touchscreenEnable or false) "NOCTALIA_GREETER_OSK=${greeterOsk} "
    + "${config.programs.noctalia-greeter.package}/bin/noctalia-greeter-session -- ${config.programs.noctalia-greeter.greeter-args}"
  );

  security.polkit.enable = true;
}
