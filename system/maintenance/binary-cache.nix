# The GjallarOS release cache from deployment/binary-cache.json. CI builds the
# heavy local packages for each release tag and signs them with this key.
# Anything missing from the cache still builds on the machine.
{ lib, ... }:
let
  cache = builtins.fromJSON (builtins.readFile ../../deployment/binary-cache.json);
in
{
  config = lib.mkIf (cache.url != "") {
    assertions = [
      {
        assertion = lib.hasPrefix "https://" cache.url && cache.publicKey != "";
        message = "deployment/binary-cache.json needs an https url and its publicKey.";
      }
    ];

    nix.settings = {
      extra-substituters = [ cache.url ];
      extra-trusted-public-keys = [ cache.publicKey ];
    };
  };
}
