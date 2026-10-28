# plymouth quit --retain-splash leaves the last frame on screen but never runs
# a script theme's quit function, so the frame left behind is a random moment
# of the animation (our mark frozen mid-turn under the greeter and unl0kr).
# The script plugin now runs the quit function and draws its frame when the
# splash goes idle, which every quit passes, like two-step does with its end
# animation. Upstream report: book 753.
# Test: checks boot-splash-retain; remove when upstream runs the quit function
# on a retained quit.
final: prev:

let
  lib = prev.lib;
in
{
  plymouth =
    assert lib.assertMsg (prev.plymouth.version == "26.134.222")
      "GjallarOS plymouth overlay was written for 26.134.222; check whether ${prev.plymouth.version} runs a script theme's quit function on quit --retain-splash and update or remove pkgs/overlays/plymouth.nix.";
    prev.plymouth.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/plymouth-script-quit-on-retain.patch ];
    });
}
