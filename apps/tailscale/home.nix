# The session side of the VPN block (ROADMAP section 16): one notice when
# the VPN blocks the network this device joined, with a pause button, and
# the shield in the bar (user/wm/shells/noctalia/plugins/vpn).
{
  config,
  lib,
  pkgs,
  ...
}:
let
  gjallarctl = pkgs.callPackage ../../pkgs/gjallarctl { };
in
lib.mkIf config.gjallar.apps.tailscale.enable {
  systemd.user.services.vpn-notice = {
    Unit = {
      Description = "Notice when the VPN blocks this network";
      After = [ "hyprland-session.target" ];
      PartOf = [ "hyprland-session.target" ];
    };
    Service = {
      Type = "exec";
      ExecStart = "${gjallarctl}/bin/gjallarctl vpn notice";
    };
    Install.WantedBy = [ "hyprland-session.target" ];
  };
  # gjallar-vpn-trust writes a new decision on every network change.
  systemd.user.paths.vpn-notice = {
    Unit = {
      Description = "Watch the VPN trust decision";
      PartOf = [ "hyprland-session.target" ];
    };
    Path.PathChanged = "/run/gjallar/vpn-trust.json";
    Install.WantedBy = [ "hyprland-session.target" ];
  };

  # The shield's click while blocked. The password dialog comes from the
  # bar's polkit agent.
  systemd.user.services.vpn-pause = {
    Unit.Description = "Pause the VPN block on this network";
    Service = {
      Type = "oneshot";
      ExecStart = "${gjallarctl}/bin/gjallarctl vpn pause";
    };
  };
  # The shield's click while paused.
  systemd.user.services.vpn-resume = {
    Unit.Description = "End the VPN pause";
    Service = {
      Type = "oneshot";
      ExecStart = "${gjallarctl}/bin/gjallarctl vpn resume";
    };
  };
}
