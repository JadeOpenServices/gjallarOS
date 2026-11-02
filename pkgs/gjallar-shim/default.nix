{
  lib,
  stdenvNoCC,
  fetchurl,
  dpkg,
  openssl,
  python3,
  sbsigntool,
}:

# The shim for firmware that only trusts the Microsoft UEFI CAs and cannot
# take our own keys (locked firmware, Plane 293, wiki 951 "Step 2").
#
# Debian 13 shim 16.1, signed by Microsoft under UEFI CA 2011 and UEFI CA
# 2023, with MokManager and fallback signed by Debian's key built into the
# shim. The files come from snapshot.debian.org by SHA-1 (the address never
# changes) and are pinned by SHA-256. Both were checked against the signed
# Debian 13.7 archive index on 2026-11-02.
#
# check-shim.py runs at build time: a changed pin has to carry the same
# signatures, or the build fails.
let
  ca2011 = "48e99b991f57fc52f76149599bff0a58c47154229b9f8d603ac40d3500248507";
  ca2023 = "f6124e34125bee3fe6d79a574eaa7b91c0e7bd9d929c1a321178efd611dad901";
  debianCA = "079646974bce09b1f04da67bd722d1fb0947ae4c4010bccdbba52d5b23cbf1a2";

  shimDeb = fetchurl {
    name = "shim-signed_1.51-1-deb13u1-16.1-2-deb13u1_amd64.deb";
    urls = [
      "https://snapshot.debian.org/file/622376cb844d11bbb2758011f48c6572564b6375"
      "https://deb.debian.org/debian/pool/main/s/shim-signed/shim-signed_1.51~1+deb13u1+16.1-2~deb13u1_amd64.deb"
    ];
    sha256 = "3c802fa303c0e6bf126adee74028d1042e3120360d159c2c05181dc9b2f61005";
  };
  helpersDeb = fetchurl {
    name = "shim-helpers-amd64-signed_1-16.1-2-deb13u1_amd64.deb";
    urls = [
      "https://snapshot.debian.org/file/5f224eaa14e6a4f3f6658e98f20349225abebabf"
      "https://deb.debian.org/debian/pool/main/s/shim-helpers-amd64-signed/shim-helpers-amd64-signed_1+16.1+2~deb13u1_amd64.deb"
    ];
    sha256 = "7e950d75d5a40ecc0a99f727452ce35780b47a04d9a2cc3b1ff7c2bb316c033e";
  };
in
stdenvNoCC.mkDerivation {
  pname = "gjallar-shim";
  version = "16.1-2-deb13u1";

  dontUnpack = true;
  nativeBuildInputs = [
    dpkg
    openssl
    python3
    sbsigntool
  ];

  buildPhase = ''
    runHook preBuild
    mkdir x work
    dpkg-deb --fsys-tarfile ${shimDeb} | tar -x -C x ./usr/lib/shim/shimx64.efi.signed
    dpkg-deb --fsys-tarfile ${helpersDeb} | tar -x -C x ./usr/lib/shim/mmx64.efi.signed ./usr/lib/shim/fbx64.efi.signed
    python3 -I ${./check-shim.py} work \
      x/usr/lib/shim/shimx64.efi.signed \
      x/usr/lib/shim/mmx64.efi.signed \
      x/usr/lib/shim/fbx64.efi.signed \
      ${debianCA} ${ca2011} ${ca2023}
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    install -Dm444 x/usr/lib/shim/shimx64.efi.signed $out/share/shim/shimx64.efi
    install -Dm444 x/usr/lib/shim/mmx64.efi.signed $out/share/shim/mmx64.efi
    install -Dm444 x/usr/lib/shim/fbx64.efi.signed $out/share/shim/fbx64.efi
    runHook postInstall
  '';

  passthru = {
    inherit ca2011 ca2023 debianCA;
  };

  meta = {
    description = "Pinned Microsoft-signed shim and MokManager for locked firmware";
    license = lib.licenses.bsd2;
    platforms = [ "x86_64-linux" ];
  };
}
