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
      ../../cmd/gjallar-namecheck
      ../../cmd/gjallar-settingsd
      ../../cmd/gjallar-snapshotd
      ../../cmd/gjallar-system-disk
      ../../internal/installer/config
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
    ];
  };

  vendorHash = "sha256-Vt+uu24eoM6iJNbf1jUJePCghiTlQpy2q4CQOLQZN/g=";
  proxyVendor = true;

  subPackages = [
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
