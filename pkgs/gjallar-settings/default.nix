{
  copyDesktopItems,
  gjallarctl,
  lib,
  makeDesktopItem,
  makeWrapper,
  python3,
  qt6,
  stdenv,
}:
let
  python = python3.withPackages (p: [ p.pyside6 ]);
  share = "share/gjallar-settings";
in
stdenv.mkDerivation {
  pname = "gjallar-settings";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
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
    substitute gjallar_settings.py "$out/${share}/gjallar_settings.py" \
      --subst-var-by gjallarctl ${lib.getExe gjallarctl} \
      --subst-var-by qml "$out/${share}/qml"
    makeWrapper ${python.interpreter} "$out/bin/gjallar-settings" \
      --add-flags "-I $out/${share}/gjallar_settings.py" \
      "''${qtWrapperArgs[@]}"
    runHook postInstall
  '';

  desktopItems = [
    (makeDesktopItem {
      name = "gjallar-settings";
      desktopName = "GjallarOS Settings";
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
      comment = "Install and remove apps from the GjallarOS catalogue";
      exec = "gjallar-settings apps";
      icon = "system-software-install";
      categories = [ "Settings" "PackageManager" ];
    })
  ];

  meta = {
    description = "GjallarOS settings window with gjallarHorn and the app catalogue";
    mainProgram = "gjallar-settings";
    license = lib.licenses.agpl3Plus;
    platforms = lib.platforms.linux;
  };
}
