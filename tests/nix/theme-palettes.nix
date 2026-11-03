{ pkgs }:
# Holds every openJade palette to the brand floors (text 12:1, dim text and
# accent 3:1 on the ground) and to the 4.5:1 the Noctalia pre-hook asks for.
# Tones Noctalia derives itself (surface_container_*) are left to that
# pre-hook at run time.
let
  inherit (pkgs) lib;
  contrast = import ../../themes/lib/contrast.nix { inherit pkgs; };
  palettes = import ../../themes/palettes/openjade.nix;

  pairs =
    name: p:
    let
      t = p.terminal;
      text = [
        "${name}-text"
        p.mSurface
        p.mOnSurface
      ];
      floor3 = [
        "${name}-dim"
        p.mSurface
        p.mOnSurfaceVariant
        "${name}-accent"
        p.mSurface
        p.mPrimary
      ];
      normal = [
        "${name}-surface-variant"
        p.mSurfaceVariant
        p.mOnSurfaceVariant
        "${name}-primary"
        p.mPrimary
        p.mOnPrimary
        "${name}-secondary"
        p.mSecondary
        p.mOnSecondary
        "${name}-tertiary"
        p.mTertiary
        p.mOnTertiary
        "${name}-error"
        p.mError
        p.mOnError
        "${name}-hover"
        p.mHover
        p.mOnHover
        "${name}-selection"
        t.selectionBg
        t.selectionFg
        "${name}-cursor"
        t.cursor
        t.cursorText
      ]
      ++ lib.concatLists (
        lib.mapAttrsToList (
          colour: value:
          [
            "${name}-bright-${colour}"
            t.background
            value
          ]
        ) t.bright
      )
      ++ lib.concatLists (
        lib.mapAttrsToList (
          colour: value:
          lib.optionals (colour != "black") [
            "${name}-${colour}"
            t.background
            value
          ]
        ) t.normal
      );
    in
    {
      inherit text floor3 normal;
    };

  all = lib.mapAttrsToList (name: p: pairs name p.dark) palettes;
  run =
    threshold: key:
    lib.escapeShellArgs (
      [
        "--threshold"
        threshold
      ]
      ++ lib.concatMap (p: p.${key}) all
    );
  guard = "${contrast.guard}/bin/gjallar-theme-contrast";
in
pkgs.runCommand "theme-palettes" { } ''
  ${guard} ${run "12" "text"}
  ${guard} ${run "3" "floor3"}
  ${guard} ${run contrast.threshold "normal"}
  touch "$out"
''
