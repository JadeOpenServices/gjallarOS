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
      ../../cmd/gjallar-storage-icon
      ../../cmd/gjallar-usbguard-boot
      ../../cmd/gjallar-usbtrustd
      ../../internal/usbtrust
    ];
  };

  vendorHash = "sha256-Mp0do2yse7kCCmyE2ACqdk0DMAX3bpF2KHjHsKDjG8o=";
  proxyVendor = true;

  subPackages = [
    "cmd/gjallar-storage-icon"
    "cmd/gjallar-usbguard-boot"
    "cmd/gjallar-usbtrustd"
  ];

  meta = {
    description = "GjallarOS privileged USB trust broker";
    license = lib.licenses.agpl3Plus;
    mainProgram = "gjallar-usbtrustd";
    platforms = lib.platforms.linux;
  };
}
