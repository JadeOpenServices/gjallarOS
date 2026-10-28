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

  # On-screen keyboard at the login screen of touchscreen machines. The
  # greeter compositor starts it with --hidden and shows it on touch.
  # --no-popup: no enlarged preview of the pressed key, which would show the
  # password to anyone watching.
  greeterOsk = pkgs.writeShellScript "gjallar-greeter-osk" ''
    exec ${pkgs.wvkbd}/bin/wvkbd-mobintl -H 320 -L 240 --no-popup "$@"
  '';

  # Noctalia re-stages the greeter look on every login and theme load, and
  # each sync is a pkexec password prompt. Ask only when the staged files
  # differ from the last answered sync. No passwordless polkit rule: the
  # greeter look is shared by every account on the machine.
  greeterSyncPrivilege = pkgs.writeShellScriptBin "gjallar-greeter-sync-privilege" ''
    set -eu
    PATH=${
      lib.makeBinPath [
        pkgs.coreutils
        pkgs.findutils
      ]
    }:$PATH
    staging="$2"
    state="''${XDG_STATE_HOME:-$HOME/.local/state}/gjallar/greeter-sync.sha256"
    sum=$(cd "$staging" && find . -maxdepth 1 -type f -print0 | sort -z \
      | xargs -0 sha256sum | sha256sum)
    if [ -r "$state" ] && [ "$(cat "$state")" = "$sum" ]; then
      exit 0
    fi
    mkdir -p "$(dirname "$state")"
    # First sync of a new account is the default look the greeter already
    # shows: record it, do not ask during the welcome flow.
    if [ ! -e "$state" ]; then
      printf '%s\n' "$sum" > "$state"
      exit 0
    fi
    # Run only the store path the org.gjallaros.greeter-sync action names;
    # the generic pkexec action is refused (local-hardening.nix).
    case "$1" in
      */noctalia-greeter-apply-appearance) shift ;;
      *) echo "greeter sync: unexpected helper $1" >&2; exit 1 ;;
    esac
    # A dismissed prompt counts as the answer for this look; the next change
    # asks again instead of re-prompting for the same files.
    rc=0
    /run/wrappers/bin/pkexec ${greeterHelper} "$@" || rc=$?
    if [ "$rc" -eq 0 ] || [ "$rc" -eq 126 ]; then
      printf '%s\n' "$sum" > "$state"
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

  services.greetd.greeterManagesPlymouth = true;

  services.greetd.settings.default_session.command = lib.mkIf (settings.touchscreenEnable or false) (
    "${pkgs.coreutils}/bin/env NOCTALIA_GREETER_OSK=${greeterOsk} "
    + "${config.programs.noctalia-greeter.package}/bin/noctalia-greeter-session --"
  );

  security.polkit.enable = true;
}
