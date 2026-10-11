final: prev:
let
  lib = prev.lib;
  capacityPatch = ../patches/kio-device-capacity-text.patch;

  patchedKdePackages = prev.kdePackages.overrideScope (
    _scopeFinal: scopePrev: {
      kio =
        assert lib.assertMsg (scopePrev.kio.version == "6.26.0")
          "GjallarOS KIO device-capacity patch targets KIO 6.26.0; review pkgs/optional-overlays/kio-device-capacity-text.nix before updating KIO.";
        scopePrev.kio.overrideAttrs (old: {
          patches = (old.patches or [ ]) ++ [ capacityPatch ];
        });
    }
  );
in
{
  kdePackages = patchedKdePackages;
}
