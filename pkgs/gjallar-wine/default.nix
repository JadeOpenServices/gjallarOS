{
  lib,
  buildGoModule,
  bubblewrap,
  passt,
  nftables,
  util-linux,
  iproute2,
  coreutils,
  _wine,
  # Wine's setup for the base prefix; without it gjallar-wine add refuses.
  wineInit ? null,
  # Adds each app's files folder to the file manager's Places.
  dolphinPlace ? null,
}:

buildGoModule {
  pname = "gjallar-wine";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../..;
    fileset = lib.fileset.unions [
      ../../go.mod
      ../../go.sum
      (lib.fileset.fileFilter (f: f.hasExt "go" || f.name == "meta.json") ../../apps)
      ../../apps/firefox-addons.json
      ../../apps/retired.json
      ../../cmd/gjallar-wine
      ../../internal/installer/config
      ../../internal/localchanges
      ../../internal/nettrust
      ../../internal/settings
      ../../internal/sshsig
      ../../internal/winesandbox
    ];
  };

  vendorHash = "sha256-Vt+uu24eoM6iJNbf1jUJePCghiTlQpy2q4CQOLQZN/g=";
  proxyVendor = true;

  subPackages = [ "cmd/gjallar-wine" ];

  # The sandbox's tools come from the store, never from PATH.
  ldflags = [
    "-X main.bwrapPath=${bubblewrap}/bin/bwrap"
    "-X main.pastaPath=${passt}/bin/pasta"
    "-X main.nftPath=${nftables}/bin/nft"
    "-X main.nsenterPath=${util-linux}/bin/nsenter"
    "-X main.ipPath=${iproute2}/bin/ip"
    "-X main.winePath=${_wine}/bin/wine"
    "-X main.cpPath=${coreutils}/bin/cp"
  ]
  ++ lib.optional (wineInit != null) "-X main.initPath=${wineInit}/bin/wine-win11-init"
  ++ lib.optional (dolphinPlace != null) "-X main.placePath=${lib.getExe dolphinPlace}";

  env.CGO_ENABLED = "0";

  meta = {
    description = "Starts a Wine app in its own sandbox";
    license = lib.licenses.agpl3Plus;
    mainProgram = "gjallar-wine";
    platforms = lib.platforms.linux;
  };
}
