{
  lib,
  buildGoModule,
}:

buildGoModule {
  pname = "gjallar-usbtrustd";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../..;
    fileset = lib.fileset.unions [
      ../../go.mod
      ../../go.sum
      ../../cmd/gjallar-usbtrustd
      ../../internal/usbtrust
    ];
  };

  vendorHash = "sha256-Vt+uu24eoM6iJNbf1jUJePCghiTlQpy2q4CQOLQZN/g=";
  proxyVendor = true;

  subPackages = [
    "cmd/gjallar-usbtrustd"
  ];

  meta = {
    description = "GjallarOS privileged USB trust broker";
    license = lib.licenses.agpl3Plus;
    mainProgram = "gjallar-usbtrustd";
    platforms = lib.platforms.linux;
  };
}
