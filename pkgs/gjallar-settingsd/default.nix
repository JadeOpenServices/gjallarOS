{
  lib,
  buildGoModule,
  openssh,
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
      ../../cmd/gjallar-devproof
      ../../cmd/gjallar-namecheck
      ../../cmd/gjallar-settingsd
      ../../cmd/gjallar-snapshotd
      ../../cmd/gjallar-system-disk
      ../../internal/devgate
      ../../internal/installer/config
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
    ];
  };

  vendorHash = "sha256-Mp0do2yse7kCCmyE2ACqdk0DMAX3bpF2KHjHsKDjG8o=";
  proxyVendor = true;

  subPackages = [
    "cmd/gjallar-devproof"
    "cmd/gjallar-namecheck"
    "cmd/gjallar-settingsd"
    "cmd/gjallar-snapshotd"
    "cmd/gjallar-system-disk"
  ];

  # Own signing keys are checked by ssh-keygen -Y verify.
  ldflags = [
    "-X github.com/JadeOpenServices/gjallarOS/internal/sshsig.Keygen=${openssh}/bin/ssh-keygen"
  ];

  env.CGO_ENABLED = "0";

  meta = {
    description = "GjallarOS root settings service";
    license = lib.licenses.agpl3Plus;
    mainProgram = "gjallar-settingsd";
    platforms = lib.platforms.linux;
  };
}
