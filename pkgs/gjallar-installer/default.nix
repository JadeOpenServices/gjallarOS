# The disk-provisioning binaries. They can repartition and reinstall a
# machine, so the installed system never carries them on its PATH: only the
# recovery specialisation, the recovery-maintenance initrd and the recovery
# ISO include this package.
#
# tzdata gives the zone and country tables the installer uses to guess the
# weather city from the timezone, without asking the network. With
# glibcLocales and xkeyboard_config it also gives the region step its lists:
# the step only accepts a timezone, locale or keyboard layout these tables
# name, so nothing typed or read from the live session reaches state.nix
# unchecked. Only the two list files are copied in (regionLists): the
# glibcLocales package alone is over 200 MiB.
#
# cryptsetup and util-linux are baked in by store path for the move
# helper, which runs them as root: nothing is looked up through PATH (L114).
# nix is baked in the same way for the store export's confined unit (L62),
# git and bash for the repository copy (L63): root's git steps and the
# confined pack unit's script.
#
# oddc is the full ODDC source (the flake's oddc input). With it the
# installer drafts a model for machines ODDC does not match; without it
# (recovery tools) it skips the draft.
{
  callPackage,
  lib,
  runCommand,
  tzdata,
  glibcLocales,
  xkeyboard_config,
  cryptsetup,
  util-linux,
  nix,
  git,
  bash,
  oddc ? null,
}:
let
  gjallarctl = callPackage ../gjallarctl { };
  regionLists = runCommand "gjallar-installer-region-lists" { } ''
    install -Dm444 ${glibcLocales}/share/i18n/SUPPORTED $out/SUPPORTED
    install -Dm444 ${xkeyboard_config}/share/X11/xkb/rules/base.lst $out/base.lst
  '';
in
gjallarctl.overrideAttrs (old: {
  pname = "gjallar-installer";

  subPackages = [
    "cmd/gjallar-installer"
    "cmd/gjallar-recovery-maintenance"
    "cmd/gjallar-stick"
    "cmd/gjallar-move-helper"
  ];

  # The installer runs gjallarctl, which no longer sits next to it.
  ldflags =
    old.ldflags
    ++ [
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/control.Path=${gjallarctl}/bin/gjallarctl"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/zonecity.Dir=${tzdata}/share/zoneinfo"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/region.ZoneDir=${tzdata}/share/zoneinfo"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/region.LocaleList=${regionLists}/SUPPORTED"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/region.XKBRules=${regionLists}/base.lst"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot.Cryptsetup=${lib.getExe' cryptsetup "cryptsetup"}"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot.Lsblk=${lib.getExe' util-linux "lsblk"}"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/move/storecopy.Nix=${lib.getExe' nix "nix"}"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/move/repocopy.Git=${lib.getExe' git "git"}"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/move/repocopy.Shell=${lib.getExe' bash "bash"}"
      "-X github.com/JadeOpenServices/gjallarOS/internal/installer/move.HelperPath=${placeholder "out"}/bin/gjallar-move-helper"
    ]
    ++ lib.optional (
      oddc != null
    ) "-X github.com/JadeOpenServices/gjallarOS/internal/installer/oddcdraft.Catalog=${oddc}";

  meta = old.meta // {
    description = "gjallarOS installer and recovery-storage maintenance";
    mainProgram = "gjallar-installer";
  };
})
