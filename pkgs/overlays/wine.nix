# The Full build embeds the Mono and Gecko installers; without them wineboot -u
# stops at a download prompt and an offline first start never ends. nixpkgs
# 26.05 pairs Wine 11.8 with wine-mono 11.0.0, but appwiz.cpl in 11.8 only
# takes 11.1.0 and asks to download it, so swap in the 11.1.0 installer.
# Test: fw13 VM, offline wine-win11-init ends without a prompt. Remove once
# nixpkgs release-26.05 ships wine-mono 11.1.0 next to 11.8.
final: prev:

let
  lib = prev.lib;
  stagingFull = prev.wineWow64Packages.stagingFull;
  wineMono = final.fetchurl {
    url = "https://dl.winehq.org/wine/wine-mono/11.1.0/wine-mono-11.1.0-x86.msi";
    hash = "sha256-3rA0FDH4Jgsgn/9rx53cxUFLl/jpI2q5+9ykzlngqbk=";
  };
in
{
  _wine =
    assert lib.assertMsg (stagingFull.version == "11.8")
      "GjallarOS Wine overlay was written for Wine 11.8 with wine-mono 11.0.0; check which wine-mono ${stagingFull.version} asks for and update or remove pkgs/overlays/wine.nix.";
    stagingFull.overrideAttrs (old: {
      postInstall = old.postInstall + ''
        rm $out/share/wine/mono/wine-mono-11.0.0-x86.msi
        ln -s ${wineMono} $out/share/wine/mono/wine-mono-11.1.0-x86.msi
      '';
    });
}
