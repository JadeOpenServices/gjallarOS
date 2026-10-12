# wvkbd with a close key, swipe down to hide, and keys that type when the
# finger lifts (a swipe that starts on a key types nothing). 0.20 adds --auto,
# which shows the keyboard only while a text field has focus.
final: prev:

let
  lib = prev.lib;
in
{
  wvkbd =
    assert lib.assertMsg (prev.wvkbd.version == "0.19.4")
      "GjallarOS wvkbd overlay moves 0.19.4 to 0.20; review or remove pkgs/overlays/wvkbd.nix now that nixpkgs has ${prev.wvkbd.version}.";
    prev.wvkbd.overrideAttrs (old: rec {
      version = "0.20";
      src = prev.fetchFromGitHub {
        owner = "jjsullivan5196";
        repo = "wvkbd";
        tag = "v${version}";
        hash = "sha256-Et1k3M8L/r5WnoNOuiCzVVgJrpXtWSbIUI+2/njiwj0=";
      };
      patches = (old.patches or [ ]) ++ [ ../patches/wvkbd-close-key-swipe.patch ];
    });
}
