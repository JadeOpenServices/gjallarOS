{ lib, pkgs, ... }:

# Keep in sync with internal/installer/resume: the installer arms
# pending.json before a planned reboot and claims it as active.json, so a
# boot that dies mid-resume runs the same transaction again.
let
  stateDir = "/var/lib/gjallarOS/installer-resume";
  installer = "${stateDir}/gjallar-installer";
in
{
  # The release-alignment wrapper adds its own copy of this unit only when
  # the target configuration lacks this module.
  options.gjallar.installerResume.enable = lib.mkOption {
    type = lib.types.bool;
    default = true;
    internal = true;
    description = "Whether the permanent installer-resume unit is defined.";
  };

  # The unit existed only in the wrapper generation built for release
  # alignment; installing gjallarOS or a later rebuild dropped it, and a
  # reboot mid-resume lost the transaction (e2e-target, 2026-10-05).
  config.systemd.services.installer-resume = {
    description = "Resume the gjallarOS installer after a planned or interrupted reboot";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [
      "local-fs.target"
      "network-online.target"
    ];
    # The continuation asks on tty1 (ODDC review, TPM2 consent); started
    # alongside the greeter it had no terminal and failed (e2e-full,
    # 2026-10-05).
    before = [
      "display-manager.service"
      "greetd.service"
    ];

    # The installer calls sudo and system tools that the default unit PATH
    # does not provide. The interactive installer gets sbctl, efibootmgr,
    # sgdisk and parted from its bootstrap configuration; the installed
    # system does not ship them (e2e-full, 2026-10-05).
    path = [
      "/run/wrappers"
      "/run/current-system/sw"
      pkgs.sbctl
      pkgs.efibootmgr
      pkgs.gptfdisk
      pkgs.parted
    ];

    unitConfig = {
      ConditionPathExists = [
        "|${stateDir}/pending.json"
        "|${stateDir}/active.json"
      ];
      ConditionFileIsExecutable = installer;
    };

    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
      TimeoutStartSec = 0;
      StandardInput = "tty";
      # Prompts end without a newline and journald holds partial lines, so
      # they never reached tty1; stdout also carries the Secure Boot
      # recovery passphrase, which must stay out of the journal (e2e-full,
      # 2026-10-05). Errors and build logs on stderr stay in the journal.
      StandardOutput = "tty";
      StandardError = "journal+console";
      TTYPath = "/dev/tty1";
      TTYReset = true;
      TTYVHangup = true;
      ExecStartPre = [
        "-${pkgs.plymouth}/bin/plymouth quit"
        "-${pkgs.kbd}/bin/chvt 1"
      ];
      ExecStart = "${installer} --resume-transaction ${stateDir}";
    };
  };
}
