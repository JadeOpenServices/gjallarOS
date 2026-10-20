{ pkgs }:
let
  inherit (pkgs) lib;
  inherit (lib) mkOption types;
  fixture = ./local-changes-fixture;
  # sha256sum manifest of the fixture, as internal/localchanges hashes it.
  fixtureHash = "d267163af43afcaba17edf0a7689fe664ff5515bde26b16039ac67cb5bd7bcd4";
  stagedSource = {
    local = {
      hash = fixtureHash;
      files = [ ];
    };
  };

  markers = mkOption {
    type = types.attrsOf types.bool;
    default = { };
  };
  # The few NixOS and home-manager options the module and the fixture use.
  # specialisation builds its child as nixos/modules/system/activation/
  # specialisation.nix does.
  stubs =
    { extendModules, ... }:
    {
      options.marker = markers;
      options.system.nixos.tags = mkOption {
        type = types.listOf types.str;
        default = [ ];
      };
      options.home-manager.users = mkOption {
        # submoduleWith, like home-manager's: definitions may hold imports.
        type = types.attrsOf (types.submoduleWith { modules = [ { options.marker = markers; } ]; });
        default = { };
      };
      options.specialisation = mkOption {
        type = types.attrsOf (
          types.submodule {
            options.configuration = mkOption {
              default = { };
              inherit (extendModules { modules = [ { specialisation = lib.mkOverride 0 { }; } ]; }) type;
            };
          }
        );
        default = { };
      };
      config.specialisation.recovery.configuration.marker.recovery = true;
    };

  evaluate =
    localDir: buildSource:
    lib.evalModules {
      specialArgs = {
        inherit buildSource;
        settings.username = "tester";
      };
      modules = [
        stubs
        (import ../../system/security/local-changes.nix { inherit localDir; })
      ];
    };
  evaluates =
    localDir: buildSource:
    (builtins.tryEval (builtins.deepSeq (evaluate localDir buildSource).config.marker true)).success;

  staged = evaluate fixture stagedSource;
  # The evaluated child, as in NixOS.
  recovery = staged.config.specialisation.recovery.configuration;
  official = (evaluate ./local-changes-missing { }).config;
in
assert
  staged.config.marker == {
    a = true;
    c = true;
  };
assert staged.config.home-manager.users.tester.marker == { b = true; };
assert staged.config.system.nixos.tags == [ "local-changes" ];
# Recovery and the other extra boot entries stay official.
assert recovery.marker == { recovery = true; };
assert recovery.home-manager.users.tester.marker == { };
assert recovery.system.nixos.tags == [ ];
assert official.marker == { } && official.system.nixos.tags == [ ];
# A folder gjallarctl did not stage, a missing folder it did stage, and a
# folder that differs from the recorded hash all stop the build.
assert !(evaluates fixture { });
assert !(evaluates ./local-changes-missing stagedSource);
assert
  !(evaluates fixture {
    local = {
      hash = builtins.replaceStrings [ "d" ] [ "e" ] fixtureHash;
      files = [ ];
    };
  });
pkgs.runCommand "gjallar-local-changes-check" { } "touch $out"
