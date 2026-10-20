final: prev:

let
  lib = prev.lib;

  qt6ctKdePatch = prev.fetchurl {
    url = "https://github.com/user-attachments/files/25205239/qt6ct-0.11.patch";
    hash = "sha256-2+HPQ7P1Yd8WFzGZP/u1vwxzVLUCEO5Lu0xcceFIOP8=";
  };

  patchedQt6Packages = prev.qt6Packages.overrideScope (
    scopeFinal: scopePrev: {
      qt6ct =
        assert lib.assertMsg (scopePrev.qt6ct.version == "0.11")
          "GjallarOS qt6ct KDE integration patch targets qt6ct 0.11; review or remove pkgs/overlays/qt6ct-kde-integration.nix before updating qt6ct.";
        scopePrev.qt6ct.overrideAttrs (old: {
          patches = (old.patches or [ ]) ++ [ qt6ctKdePatch ];

          buildInputs = (old.buildInputs or [ ]) ++ [
            scopeFinal.qtdeclarative
            prev.kdePackages.kconfig
            prev.kdePackages.kcolorscheme
            prev.kdePackages.kiconthemes
          ];

          # The patch switches QML apps to the org.kde.desktop style, so that
          # style has to sit next to qt6ct in the profile, as Arch's qt6ct-kde
          # depends on qqc2-desktop-style. Kirigami and Sonnet are its QML
          # imports; nixpkgs propagates neither into a user profile, and its
          # kirigami attribute is a build-time wrapper without the QML files.
          propagatedUserEnvPkgs = (old.propagatedUserEnvPkgs or [ ]) ++ [
            prev.kdePackages.qqc2-desktop-style
            prev.kdePackages.kirigami.unwrapped
            prev.kdePackages.sonnet
          ];
        });
    }
  );
in
{
  qt6Packages = patchedQt6Packages;

  # Keep both public package namespaces on the exact same patched derivation.
  kdePackages = prev.kdePackages.overrideScope (
    _scopeFinal: _scopePrev: {
      qt6ct = final.qt6Packages.qt6ct;
    }
  );
}
