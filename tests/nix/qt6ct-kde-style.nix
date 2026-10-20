{ pkgs }:
let
  # home.packages becomes a buildEnv; only propagated-user-env-packages come along.
  profile = pkgs.buildEnv {
    name = "gjallar-qt6ct-profile";
    paths = [ pkgs.qt6Packages.qt6ct ];
  };
in
pkgs.runCommand "gjallar-qt6ct-kde-style-check" { } ''
  # qt6ct's KDE patch picks org.kde.desktop for QML apps. Without the style
  # in the profile, Nextcloud's "Add account" did nothing (2026-10-08).
  for d in org/kde/desktop org/kde/kirigami org/kde/sonnet; do
    test -f ${profile}/lib/qt-6/qml/$d/qmldir || { echo "missing QML module $d"; exit 1; }
  done
  test -e ${profile}/lib/qt-6/plugins/kf6/kirigami/platform/org.kde.desktop.so
  touch $out
''
