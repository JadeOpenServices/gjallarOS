{
  lib,
  buildGoModule,
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
      ../../cmd/gjallar-namecheck
      ../../cmd/gjallar-settingsd
      ../../internal/installer/config
      ../../internal/installer/systemcheckout
      ../../internal/localchanges
      ../../internal/namecheck
      ../../internal/nettrust
      ../../internal/noctalia
      ../../internal/settings
    ];
  };

  vendorHash = "sha256-Vt+uu24eoM6iJNbf1jUJePCghiTlQpy2q4CQOLQZN/g=";
  proxyVendor = true;

  subPackages = [
    "cmd/gjallar-namecheck"
    "cmd/gjallar-settingsd"
  ];

  env.CGO_ENABLED = "0";

  meta = {
    description = "GjallarOS root settings service";
    license = lib.licenses.agpl3Plus;
    mainProgram = "gjallar-settingsd";
    platforms = lib.platforms.linux;
  };
}
