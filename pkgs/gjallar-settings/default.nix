{
  copyDesktopItems,
  gjallarctl,
  lib,
  makeDesktopItem,
  makeWrapper,
  pkgs,
  python3,
  qt6,
  stdenv,
}:
let
  python = python3.withPackages (p: [ p.pyside6 ]);
  share = "share/gjallar-settings";
  # Repo app icons, apps/<id>/icon.svg (Plane 273); checked below.
  appIcons = lib.fileset.toSource {
    root = ../../apps;
    fileset = lib.fileset.fileFilter (file: file.name == "icon.svg") ../../apps;
  };
  # What the detail pages show besides the catalogue text (Plane 276):
  # licence and homepage from the app's package meta, and whether the app
  # changes the system (apps/<id>/nixos.nix). Built here so the window
  # never fetches anything; only https homepages are kept.
  licenceNames =
    l: if l ? licenses then lib.concatMap licenceNames l.licenses else [ (l.spdxId or l.fullName or "unknown") ];
  appDetails = lib.mapAttrs (
    _: app:
    let
      meta = if app ? package then (lib.getAttrFromPath (lib.splitString "." app.package) pkgs).meta or { } else { };
    in
    {
      licences = lib.unique (lib.concatMap licenceNames (lib.toList (meta.license or [ ])));
      homepage = lib.findFirst (lib.hasPrefix "https://") "" (lib.toList (meta.homepage or [ ]));
      system = builtins.pathExists (app.path + "/nixos.nix");
    }
  ) (import ../../apps);
  detailsFile = builtins.toFile "app-details.json" (builtins.toJSON appDetails);
in
stdenv.mkDerivation {
  pname = "gjallar-settings";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./_gjallar-settings
      ./check_icon.py
      ./gjallar_settings.py
      ./qml
    ];
  };

  nativeBuildInputs = [
    copyDesktopItems
    makeWrapper
    qt6.wrapQtAppsHook
  ];
  # The QML modules, the Wayland platform and SVG icons; the platform
  # theme (qt6ct) comes from the system profile.
  buildInputs = [
    qt6.qtbase
    qt6.qtdeclarative
    qt6.qtsvg
    qt6.qtwayland
  ];

  dontConfigure = true;
  dontBuild = true;
  # wrapQtAppsHook only wraps ELF files; the wrapper below gets its
  # arguments instead.
  dontWrapQtApps = true;

  installPhase = ''
    runHook preInstall
    install -Dm444 -t "$out/${share}/qml" qml/*.qml
    mkdir -p "$out/${share}/icons"
    for icon in ${appIcons}/*/icon.svg; do
      [ -e "$icon" ] || continue
      ${python.interpreter} -I check_icon.py "$icon"
      id=$(basename "$(dirname "$icon")")
      install -Dm444 "$icon" "$out/${share}/icons/$id.svg"
    done
    install -Dm444 ${detailsFile} "$out/${share}/app-details.json"
    substitute gjallar_settings.py "$out/${share}/gjallar_settings.py" \
      --subst-var-by details "$out/${share}/app-details.json" \
      --subst-var-by gjallarctl ${lib.getExe gjallarctl} \
      --subst-var-by icons "$out/${share}/icons" \
      --subst-var-by qml "$out/${share}/qml"
    makeWrapper ${python.interpreter} "$out/bin/gjallar-settings" \
      --add-flags "-I $out/${share}/gjallar_settings.py" \
      "''${qtWrapperArgs[@]}"
    install -Dm444 _gjallar-settings "$out/share/zsh/site-functions/_gjallar-settings"
    runHook postInstall
  '';

  desktopItems = [
    (makeDesktopItem {
      name = "gjallar-settings";
      desktopName = "gjallarOS Settings";
      comment = "Security checks and apps for this device";
      exec = "gjallar-settings";
      icon = "preferences-system";
      categories = [ "Settings" ];
    })
    (makeDesktopItem {
      name = "gjallar-settings-security";
      desktopName = "gjallarHorn";
      comment = "Security checks for this device and what to do about them";
      exec = "gjallar-settings security";
      icon = "security-high";
      categories = [ "Settings" "Security" ];
    })
    (makeDesktopItem {
      name = "gjallar-settings-apps";
      desktopName = "Apps";
      genericName = "App catalogue";
      comment = "Install and remove apps from the gjallarOS catalogue";
      exec = "gjallar-settings apps";
      icon = "system-software-install";
      categories = [ "Settings" "PackageManager" ];
    })
  ];

  meta = {
    description = "gjallarOS settings window with gjallarHorn and the app catalogue";
    mainProgram = "gjallar-settings";
    license = lib.licenses.agpl3Plus;
    platforms = lib.platforms.linux;
  };
}
