{ config, lib, ... }:
# Every rename in one table (Jade Q166): commands, settings keys, config
# fields, labels, units and paths. A row holds the old name, the new name,
# where it was removed, the release tag after which the row and every last
# trace of the old name go, and the Plane or JODS item. The flake check
# legacy-names fails when an old name is used outside this folder or the
# release in system/release is newer than a row's tag.
#
# Old commands are not run: the shell prints the new name and stops.
let
  inherit (import ./table.nix { inherit lib; }) commands;

  hintLine =
    row: "${lib.escapeShellArg row.old}) printf '%s\\n' ${lib.escapeShellArg "gjallarOS: ${row.old} was removed; use ${row.new}"} >&2 ;;";
  hints = lib.concatMapStringsSep "\n    " hintLine commands;

  # bash and zsh print their own "not found" line only when no handler
  # exists, so the fallback prints the same line as the shell would.
  handler = name: shell: ''
    ${name}() {
      case "$1" in
        ${hints}
        *) printf '${shell}: command not found: %s\n' "$1" >&2 ;;
      esac
      return 127
    }
  '';
in
{
  # Both define the same function name; the later one would silently win.
  assertions = [
    {
      assertion = !config.programs.command-not-found.enable;
      message = "system/tools/legacy defines command_not_found_handler; programs.command-not-found would replace it";
    }
  ];

  programs.zsh.interactiveShellInit = handler "command_not_found_handler" "zsh";
  programs.bash.interactiveShellInit = handler "command_not_found_handle" "bash";

  # An old command that is a real command again would run before the hint.
  # system-path fails to build instead.
  environment.extraSetup = lib.concatMapStrings (row: ''
    if [ -e "$out/bin/"${lib.escapeShellArg row.old} ]; then
      echo ${lib.escapeShellArg "system/tools/legacy: ${row.old} was renamed to ${row.new} (${row.item}) but is a command again"} >&2
      exit 1
    fi
  '') commands;
}
