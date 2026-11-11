# The disk-provisioning binaries. They can repartition and reinstall a
# machine, so the installed system never carries them on its PATH: only the
# recovery specialisation, the recovery-maintenance initrd and the recovery
# ISO include this package.
#
# oddc is the full ODDC source (the flake's oddc input). With it the
# installer drafts a model for machines ODDC does not match; without it
# (recovery tools) it skips the draft.
{
  callPackage,
  lib,
  oddc ? null,
}:
let
  gjallarctl = callPackage ../gjallarctl { };
in
gjallarctl.overrideAttrs (old: {
  pname = "gjallar-installer";

  subPackages = [
    "cmd/gjallar-installer"
    "cmd/gjallar-recovery-maintenance"
  ];

  # The installer runs gjallarctl, which no longer sits next to it.
  ldflags = old.ldflags ++ [
    "-X github.com/JadeOpenServices/gjallarOS/internal/installer/control.Path=${gjallarctl}/bin/gjallarctl"
  ]
  ++ lib.optional (
    oddc != null
  ) "-X github.com/JadeOpenServices/gjallarOS/internal/installer/oddcdraft.Catalog=${oddc}";

  meta = old.meta // {
    description = "gjallarOS installer and recovery-storage maintenance";
    mainProgram = "gjallar-installer";
  };
})
