# LibreOffice 26.8.2.2 as libreoffice-fresh. The pinned nixpkgs has 26.2.1.2,
# which lacks the fixes from the LibreOffice security advisory of
# 2026-11-05 (fixed in 26.2.6 and 26.8.1), one of them in PDF font embedding.
#
# pkgs/libreoffice is the nixpkgs master packaging (commit in
# pkgs/libreoffice/NIXPKGS_COMMIT) with only the 26.8 build kept. Remove this
# overlay and pkgs/libreoffice once the nixpkgs pin ships 26.8.1 or newer;
# the assert below stops the build when the pin moves past 26.2.1.2.
#
# Updating pkgs/libreoffice/src for a new release: write the version to
# version.nix; nix-prefetch-url the main, help and translations tarballs
# from download.documentfoundation.org/libreoffice/src/<x.y.z>/ into
# main.nix, help.nix and translations.nix and compare each with the .sha256
# file next to it; then run nixpkgs' generate-libreoffice-srcs.py (same
# commit) with downloadList set to the download.lst from the main tarball
# and write its output to deps.nix.
final: prev:
let
  lib = prev.lib;
in
{
  libreoffice-fresh =
    assert lib.assertMsg (prev.libreoffice-fresh.version == "26.2.1.2")
      "nixpkgs moved libreoffice-fresh past 26.2.1.2; check whether it has 26.8.1 or newer and remove pkgs/overlays/libreoffice.nix and pkgs/libreoffice.";
    lib.lowPrio (
      final.callPackage ../libreoffice/wrapper.nix {
        unwrapped = final.callPackage ../libreoffice { };
      }
    );
  libreoffice-fresh-unwrapped = final.libreoffice-fresh.unwrapped;
}
