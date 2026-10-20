{ pkgs }:
let
  allowed =
    context:
    builtins.all (a: a.assertion)
      (import ../../system/security/build-entrypoint.nix { buildContext = context; }).assertions;
  rev = "0123456789abcdef0123456789abcdef01234567";
in
assert !(allowed null);
assert
  !(allowed {
    schemaVersion = 1;
    entrypoint = "nixos-rebuild";
  });
assert
  !(allowed {
    schemaVersion = 2;
    entrypoint = "gjallarctl";
  });
assert
  !(allowed {
    schemaVersion = 1;
    entrypoint = "gjallarctl";
    extra = true;
  });
assert allowed {
  schemaVersion = 1;
  entrypoint = "gjallarctl";
};
assert allowed {
  schemaVersion = 1;
  entrypoint = "gjallarctl";
  source = {
    revision = rev + "-dirty";
    oddc = rev;
    oddcPinned = rev;
  };
};
assert
  !(allowed {
    schemaVersion = 1;
    entrypoint = "gjallarctl";
    source.revision = "main";
  });
assert
  !(allowed {
    schemaVersion = 1;
    entrypoint = "gjallarctl";
    source.oddc = "github:someone/oddc";
  });
assert
  !(allowed {
    schemaVersion = 1;
    entrypoint = "gjallarctl";
    source.branch = "main";
  });
pkgs.runCommand "gjallar-build-entrypoint-check" { } "touch $out"
