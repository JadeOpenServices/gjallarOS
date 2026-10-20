# Prevent accidental deployment that skips the wrapper's preparation checks.
# Root and users who can edit the source can change this policy.
{
  buildContext ? null,
  ...
}:
let
  hex40 = value: builtins.isString value && builtins.match "[0-9a-f]{40}" value != null;
  # What gjallarctl knows about the source (flakesource.BuildSource); every
  # field is optional, and a field that is there must be a commit.
  source = buildContext.source or { };
  sourceValid =
    builtins.isAttrs source
    && builtins.all (name: builtins.elem name [ "revision" "oddc" "oddcPinned" ]) (
      builtins.attrNames source
    )
    && (
      !(source ? revision) || builtins.match "[0-9a-f]{40}(-dirty)?" (toString source.revision) != null
    )
    && (!(source ? oddc) || hex40 source.oddc)
    && (!(source ? oddcPinned) || hex40 source.oddcPinned);
in
{
  assertions = [
    {
      assertion =
        builtins.isAttrs buildContext
        &&
          removeAttrs buildContext [ "source" ] == {
            schemaVersion = 1;
            entrypoint = "gjallarctl";
          }
        && sourceValid;
      message = "Direct GjallarOS flake builds are disabled. Use rebuild (or gjallarctl installer deploy); these commands prepare and validate a staged source.";
    }
  ];
}
