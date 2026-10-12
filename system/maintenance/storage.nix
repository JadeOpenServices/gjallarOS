{ ... }:

let
  GiB = 1024 * 1024 * 1024;
in
{
  nix.gc = {
    automatic = true;
    dates = "weekly";
    persistent = true;
    randomizedDelaySec = "2h";
    options = "--delete-older-than 14d";
  };

  nix.settings = {
    min-free = 10 * GiB;
    max-free = 30 * GiB;

    auto-optimise-store = false;

    keep-outputs = false;

    keep-derivations = true;
  };

  nix.optimise = {
    automatic = true;
    dates = [ "weekly" ];
  };

  boot.tmp.cleanOnBoot = true;

  services.journald.extraConfig = ''
    SystemMaxUse=1G
    RuntimeMaxUse=256M
    MaxRetentionSec=14day
  '';

  # Builds yield to the desktop. With default weights a rebuild's 16 build
  # jobs got as much CPU as the whole user session, and a new VSCodium
  # window took 14s to appear (2026-10-08). Weights only matter under
  # contention, so idle builds still use every core.
  nix.daemonCPUSchedPolicy = "batch";
  nix.daemonIOSchedClass = "best-effort";
  nix.daemonIOSchedPriority = 7;
  # Out of memory, the kernel kills a build before the desktop. User session
  # processes run at 200 and build workers inherited 0, so a compile on a
  # 4 GiB Galaxy Book 12-like VM killed dbus-broker, Noctalia and Hyprland
  # clients first and ended the session (2026-10-12). A failed build can
  # be rerun; lost windows cannot.
  systemd.services.nix-daemon.serviceConfig = {
    CPUWeight = 20;
    IOWeight = 20;
    OOMScoreAdjust = 1000;
  };

  systemd.services.nix-gc.serviceConfig = {
    Nice = 19;
    IOSchedulingClass = "idle";
  };

  systemd.services.nix-optimise.serviceConfig = {
    Nice = 19;
    IOSchedulingClass = "idle";
  };
}
