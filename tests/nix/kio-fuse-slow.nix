# pkgs/overlays/kio.nix next to a stock KIO. A file whose name says nothing
# but whose content is a PNG sits on a FUSE mount and on a local disk. The
# patched KIO calls the FUSE mount slow and takes the type from the name
# only, so it never reads the file; the local copy is still read. Both the
# file item and the file worker's stat are checked: Dolphin uses both. The stock
# KIO shows the bug; when it stops reading the FUSE file, the test says to
# drop the overlay.
{ pkgs, stockKio }:
let
  probe =
    name: kio:
    pkgs.stdenv.mkDerivation {
      name = "gjallar-kio-slow-${name}";
      dontUnpack = true;
      nativeBuildInputs = [
        pkgs.pkg-config
        pkgs.kdePackages.wrapQtAppsHook
      ];
      buildInputs = [
        pkgs.kdePackages.qtbase
        kio
      ];
      buildPhase = ''
        c++ ${../kio-slow.cpp} -o kio-slow-${name} \
          $(pkg-config --cflags --libs Qt6Core) \
          -I${kio.dev}/include/KF6/KIOCore \
          -I${kio.dev}/include/KF6/KIO \
          -I${pkgs.kdePackages.kcoreaddons.dev}/include/KF6/KCoreAddons \
          -L${kio}/lib -lKF6KIOCore \
          -L${pkgs.kdePackages.kcoreaddons}/lib -lKF6CoreAddons
      '';
      installPhase = "mkdir -p $out/bin; cp kio-slow-${name} $out/bin/";
    };
in
pkgs.testers.runNixOSTest {
  name = "kio-fuse-slow";

  nodes.machine = {
    environment.systemPackages = [
      pkgs.bindfs
      (probe "patched" pkgs.kdePackages.kio)
      (probe "stock" stockKio)
    ];
  };

  testScript = ''
    machine.wait_for_unit("multi-user.target")
    machine.succeed(
        "mkdir -p /srv/data /mnt/fuse",
        "printf '\\211PNG\\r\\n\\032\\n' > /srv/data/picture",
        "cp /srv/data/picture /root/picture",
        "bindfs -o subtype=bindfs /srv/data /mnt/fuse",
        "findmnt -n -o FSTYPE /mnt/fuse | grep -qx fuse.bindfs",
    )


    def probe(which, path):
        return machine.succeed(f"kio-slow-{which} {path}").strip()


    with subtest("a file on a FUSE mount is typed by its name"):
        seen = probe("patched", "/mnt/fuse/picture")
        assert seen == "slow application/octet-stream", seen

    with subtest("a file on a local disk is still read"):
        seen = probe("patched", "/root/picture")
        assert seen == "fast image/png", seen

    with subtest("the file worker types a FUSE file by its name"):
        seen = probe("patched", "--stat /mnt/fuse/picture")
        assert seen == "stat application/octet-stream", seen

    with subtest("the file worker still reads a local file"):
        seen = probe("patched", "--stat /root/picture")
        assert seen == "stat image/png", seen

    item = probe("stock", "/mnt/fuse/picture")
    stat = probe("stock", "--stat /mnt/fuse/picture")
    print(f"stock: {item}; {stat}")
    if item != "fast image/png" and stat != "stat image/png":
        raise Exception(
            "stock KIO no longer reads files on a FUSE mount to type them;"
            " drop pkgs/overlays/kio.nix"
        )
  '';
}
