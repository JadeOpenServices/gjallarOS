{
  lib,
  stdenvNoCC,
  makeWrapper,
  coreutils,
  dosfstools,
  efibootmgr,
  findutils,
  gnugrep,
  gnused,
  openssl,
  sbctl,
  sbsigntool,
  util-linux,
  xorriso,
}:

# install-partition.sh with its tools pinned, so the installer does not
# depend on what the running system has in PATH: installer-resume.service
# found no xorriso there (e2e-full, 2026-10-05).
let
  path = lib.makeBinPath [
    coreutils
    dosfstools
    efibootmgr
    findutils
    gnugrep
    gnused
    openssl
    sbctl
    sbsigntool
    util-linux
    xorriso
  ];
in
stdenvNoCC.mkDerivation {
  pname = "gjallar-recovery-install";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../../scripts/recovery;
    fileset = lib.fileset.unions [
      ../../scripts/recovery/boot-first.sh
      ../../scripts/recovery/install-partition.sh
      ../../scripts/recovery/verify-image.sh
    ];
  };

  nativeBuildInputs = [ makeWrapper ];
  dontBuild = true;

  installPhase = ''
    runHook preInstall
    install -Dm0755 boot-first.sh "$out/libexec/gjallar-recovery/boot-first.sh"
    install -Dm0755 install-partition.sh "$out/libexec/gjallar-recovery/install-partition.sh"
    install -Dm0755 verify-image.sh "$out/libexec/gjallar-recovery/verify-image.sh"
    makeWrapper "$out/libexec/gjallar-recovery/install-partition.sh" "$out/bin/gjallar-recovery-install" \
      --set PATH ${path}
    makeWrapper "$out/libexec/gjallar-recovery/boot-first.sh" "$out/bin/gjallar-boot-first" \
      --set PATH ${path}
    runHook postInstall
  '';

  meta.mainProgram = "gjallar-recovery-install";
}
