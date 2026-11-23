{
  lib,
  buildGoModule,
  openssh,
  systemd,
}:

buildGoModule {
  pname = "gjallar-settingsd";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../..;
    fileset = lib.fileset.unions [
      ../../go.mod
      ../../go.sum
      (lib.fileset.fileFilter (f: f.hasExt "go" || f.name == "meta.json") ../../apps)
      ../../apps/firefox-addons.json
      ../../apps/retired.json
      ../../cmd/gjallar-chip-check
      ../../cmd/gjallar-devproof
      ../../cmd/gjallar-namecheck
      ../../cmd/gjallar-settingsd
      ../../cmd/gjallar-snapshotd
      ../../cmd/gjallar-system-disk
      ../../internal/devgate
      ../../internal/installer/config
      ../../internal/input/xkb
      ../../internal/installer/diskcrypto
      ../../internal/installer/systemcheckout
      ../../internal/installer/targetdisk
      ../../internal/localchanges
      ../../internal/namecheck
      ../../internal/nettrust
      ../../internal/noctalia
      ../../internal/settings
      ../../internal/snapshots
      ../../internal/sshsig
      ../../internal/usbtrust
      ../../internal/winesandbox
      ../../system/tools/legacy/legacy.go
      ../../system/tools/legacy/renames.json
    ];
  };

  vendorHash = "sha256-MN2ReRzA64XHTzqaCIYwC8Tzb7IZGa+WFd1nP8Idu1M=";
  proxyVendor = true;

  subPackages = [
    "cmd/gjallar-chip-check"
    "cmd/gjallar-devproof"
    "cmd/gjallar-namecheck"
    "cmd/gjallar-settingsd"
    "cmd/gjallar-snapshotd"
    "cmd/gjallar-system-disk"
  ];

  # Own signing keys are checked by ssh-keygen -Y verify. gjallar-chip-check
  # asks systemd-pcrlock (as root, by store path) whether the TPM can carry
  # the startup policy.
  ldflags = [
    "-X github.com/JadeOpenServices/gjallarOS/internal/sshsig.Keygen=${openssh}/bin/ssh-keygen"
    "-X github.com/JadeOpenServices/gjallarOS/internal/installer/diskcrypto.PCRLock=${systemd}/lib/systemd/systemd-pcrlock"
  ];

  env.CGO_ENABLED = "0";

  meta = {
    description = "gjallarOS root settings service";
    license = lib.licenses.agpl3Plus;
    mainProgram = "gjallar-settingsd";
    platforms = lib.platforms.linux;
  };
}
