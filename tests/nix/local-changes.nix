{ pkgs }:
let
  inherit (pkgs) lib;
  fixture = ./local-changes-fixture;
  # sha256sum manifest of the fixture, as internal/localchanges hashes it.
  fixtureHash = "d267163af43afcaba17edf0a7689fe664ff5515bde26b16039ac67cb5bd7bcd4";

  module =
    localDir: buildSource:
    (import ../../system/security/local-changes.nix { inherit localDir; }) {
      inherit lib buildSource;
      settings.username = "tester";
    };
  evaluates =
    localDir: buildSource:
    (builtins.tryEval (builtins.deepSeq (module localDir buildSource) true)).success;

  staged = module fixture {
    local = {
      hash = fixtureHash;
      files = [ ];
    };
  };
  official = module ./local-changes-missing { };
in
assert
  staged.imports == [
    (fixture + "/system/a.nix")
    (fixture + "/system/sub/c.nix")
  ];
assert staged.home-manager.users.tester.imports == [ (fixture + "/home/b.nix") ];
assert staged.system.nixos.tags == [ "local-changes" ];
assert official.imports == [ ] && official.system.nixos.tags == [ ];
# A folder gjallarctl did not stage, a missing folder it did stage, and a
# folder that differs from the recorded hash all stop the build.
assert !(evaluates fixture { });
assert
  !(evaluates ./local-changes-missing {
    local = {
      hash = fixtureHash;
      files = [ ];
    };
  });
assert
  !(evaluates fixture {
    local = {
      hash = builtins.replaceStrings [ "d" ] [ "e" ] fixtureHash;
      files = [ ];
    };
  });
pkgs.runCommand "gjallar-local-changes-check" { } "touch $out"
