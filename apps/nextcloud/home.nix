{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  dolphinNextcloudIcons = import ../../themes/apps/dolphin/nextcloud-icons.nix;

  enable = settings.nextcloudEnable or false;
  host = settings.nextcloudHost or "";
  preferredBrowser = settings.preferredBrowser or "firefox";
  loginRecipe =
    config.gjallar.loginBrowsers.${preferredBrowser}
      or (throw "Nextcloud login needs gjallar.loginBrowsers.${preferredBrowser}; add it in user/browsers/${preferredBrowser}.nix");
  # Hyprland floats and centres windows of this class (see below).
  loginClass = "gjallar-nextcloud-login";

  inherit (import ./package.nix { inherit pkgs; }) openvfs client;

  # The login window is its own browser with a throwaway profile in
  # $XDG_RUNTIME_DIR/nextcloud-login/<session>, run in the scope
  # nextcloud-login-<session>. Stopping the scope closes the window.
  loginSession = ''
    if ! printf '%s\n' "$session" \
      | ${pkgs.gnugrep}/bin/grep -Eqx '[A-Za-z0-9._-]+'
    then
      echo "invalid Nextcloud auth session identifier." >&2
      exit 64
    fi
    runtime="''${XDG_RUNTIME_DIR:-/run/user/$(${pkgs.coreutils}/bin/id -u)}"
    base="$runtime/nextcloud-login"
    unit="nextcloud-login-$session.scope"
    systemctl=${pkgs.systemd}/bin/systemctl
  '';

  nextcloudLoginRun = pkgs.writeShellScript "nextcloud-login-run" ''
    set -eu
    session="''${1:-}"
    ${loginSession}
    IFS= read -r url <"$base/$session/url"
    exec ${pkgs.systemd}/bin/systemd-run --user --scope --collect --quiet \
      --unit "$unit" -- ${loginRecipe} "$base/$session/profile" ${loginClass} "$url"
  '';

  nextcloudBrowserCloser = pkgs.writeShellScript "nextcloud-browser-close" ''
    set -u
    session="''${NEXTCLOUD_AUTH_SESSION:-}"
    ${loginSession}
    if $systemctl --user --quiet is-active "$unit"; then
      $systemctl --user stop "$unit"
    fi
    ${pkgs.coreutils}/bin/rm -rf "$base/$session"
    ${pkgs.coreutils}/bin/rmdir --ignore-fail-on-non-empty "$base" 2>/dev/null
    exit 0
  '';

  # The client sends every link it opens here while it runs. Only the login
  # page of the configured server goes to the login window.
  nextcloudBrowserOpener = pkgs.writeShellScript "nextcloud-browser-open" ''
    set -u
    if [ "$#" -ne 1 ]; then
      echo "Nextcloud browser opener expects exactly one URL." >&2
      exit 64
    fi

    url="$1"
    host=${lib.escapeShellArg host}
    case "$url" in
      "$host"/login/v2/flow/* | "$host"/index.php/login/v2/flow/*) ;;
      https://*)
        exec ${pkgs.xdg-utils}/bin/xdg-open "$url"
        ;;
      *)
        echo "Nextcloud browser opener rejected non-HTTPS URL." >&2
        exit 64
        ;;
    esac

    session="''${NEXTCLOUD_AUTH_SESSION:-}"
    ${loginSession}
    ${pkgs.coreutils}/bin/install -d -m 0700 "$base"

    # Logins whose window the user closed leave their profile behind.
    for dir in "$base"/*/; do
      [ -d "$dir" ] || continue
      name="$(${pkgs.coreutils}/bin/basename "$dir")"
      if ! $systemctl --user --quiet is-active "nextcloud-login-$name.scope"; then
        ${pkgs.coreutils}/bin/rm -rf "$dir"
      fi
    done

    # A second login in the same client run replaces the first window.
    if $systemctl --user --quiet is-active "$unit"; then
      $systemctl --user stop "$unit"
    fi
    ${pkgs.coreutils}/bin/rm -rf "$base/$session"
    ${pkgs.coreutils}/bin/install -d -m 0700 "$base/$session"
    printf '%s\n' "$url" >"$base/$session/url"

    workspace="''${NEXTCLOUD_WORKSPACE:-}"
    if [ -n "''${HYPRLAND_INSTANCE_SIGNATURE:-}" ]; then
      rule=""
      if printf '%s\n' "$workspace" | ${pkgs.gnugrep}/bin/grep -Eqx '[1-9][0-9]*'; then
        rule="[workspace $workspace] "
      fi
      exec ${lib.getExe' pkgs.hyprland "hyprctl"} dispatch exec \
        "$rule${nextcloudLoginRun} $session" >/dev/null
    fi
    exec ${nextcloudLoginRun} "$session"
  '';

  nextcloudConfigPolicy = pkgs.writeShellScript "nextcloud-config-policy" ''
    exec ${lib.getExe pkgs.python3} - "$@" <<'PY'
import os
import re
import sys
from pathlib import Path

if len(sys.argv) != 2:
    print("usage: nextcloud-config-policy <config>", file=sys.stderr)
    raise SystemExit(64)

path = Path(os.path.expanduser(sys.argv[1]))

path.parent.mkdir(parents=True, exist_ok=True)
try:
    text = path.read_text(encoding="utf-8", errors="replace")
except FileNotFoundError:
    text = ""

lines = text.splitlines()
required = {
    "showMainDialogAsNormalWindow": "true",
    "isVfsEnabled": "true",
}

section_start = None
section_end = len(lines)
for index, line in enumerate(lines):
    if line.strip() == "[General]":
        section_start = index
        for end in range(index + 1, len(lines)):
            stripped = lines[end].strip()
            if stripped.startswith("[") and stripped.endswith("]"):
                section_end = end
                break
        break

if section_start is None:
    prefix = ["[General]"] + [f"{key}={value}" for key, value in required.items()]
    lines = prefix + ([""] if lines else []) + lines
else:
    seen = set()
    for index in range(section_start + 1, section_end):
        if "=" not in lines[index]:
            continue
        key = lines[index].split("=", 1)[0].strip()
        if key in required:
            lines[index] = f"{key}={required[key]}"
            seen.add(key)
    for key in reversed([key for key in required if key not in seen]):
        lines.insert(section_start + 1, f"{key}={required[key]}")
        section_end += 1

output = "\n".join(lines).rstrip("\n") + "\n"
tmp = path.with_name(f".{path.name}.policy-{os.getpid()}")
tmp.write_text(output, encoding="utf-8")
os.chmod(tmp, 0o600)
os.replace(tmp, path)
PY
  '';

  nextcloudFolderPolicy = pkgs.writeShellScript "nextcloud-folder-policy" ''
    exec ${lib.getExe pkgs.python3} - "$@" <<'PY'
import os
import re
import sys

if len(sys.argv) != 3:
    print("usage: nextcloud-folder-policy <config> <canonical-root>", file=sys.stderr)
    raise SystemExit(64)

config, canonical_root = sys.argv[1:]
canonical_root = os.path.realpath(os.path.expanduser(canonical_root))

home = os.path.dirname(canonical_root)
try:
    entries = os.scandir(home)
except FileNotFoundError:
    entries = ()

for entry in entries:
    if not entry.is_dir(follow_symlinks=False):
        continue
    if not re.fullmatch(r"Nextcloud(?:[ _-]?\d+| \(\d+\))", entry.name):
        continue
    duplicate = os.path.realpath(entry.path)
    if duplicate == canonical_root:
        continue
    print(
        "Nextcloud policy refused: duplicate-looking local root exists:\n"
        f"  duplicate: {duplicate}\n"
        f"  canonical: {canonical_root}",
        file=sys.stderr,
    )
    raise SystemExit(78)

try:
    with open(config, "r", encoding="utf-8", errors="replace") as handle:
        lines = handle.readlines()
except FileNotFoundError:
    lines = []

accounts = set()
folders = {}

for raw in lines:
    line = raw.rstrip("\r\n")
    if "=" not in line:
        continue

    key, value = line.split("=", 1)

    match = re.fullmatch(r"(\d+)\\url", key)
    if match and value.strip():
        accounts.add(match.group(1))
        continue

    match = re.fullmatch(
        r"(\d+)\\(Folders|FoldersWithPlaceholders)\\(\d+)\\"
        r"(localPath|targetPath|virtualFilesMode)",
        key,
    )
    if not match:
        continue

    account, folder_kind, folder, field = match.groups()
    fields = folders.setdefault((account, folder), {})

    previous_kind = fields.get("_kind")
    if previous_kind is not None and previous_kind != folder_kind:
        print(
            "Nextcloud policy refused: conflicting folder storage groups "
            f"for account {account}, folder {folder}.",
            file=sys.stderr,
        )
        raise SystemExit(78)

    fields["_kind"] = folder_kind
    fields[field] = value.strip()

if not accounts:
    if os.path.isdir(canonical_root):
        try:
            has_content = next(os.scandir(canonical_root), None) is not None
        except OSError as exc:
            print(
                f"Nextcloud policy refused: cannot inspect canonical root: {exc}",
                file=sys.stderr,
            )
            raise SystemExit(78)
        if has_content:
            print(
                "Nextcloud policy refused: unconfigured canonical root is not empty; "
                "remove or relocate stale local data before fresh VFS enrollment.",
                file=sys.stderr,
            )
            raise SystemExit(78)
    raise SystemExit(10)

if len(accounts) != 1:
    print(
        "Nextcloud policy refused: exactly one configured account is supported; "
        f"found {len(accounts)}.",
        file=sys.stderr,
    )
    raise SystemExit(78)

if not folders:
    raise SystemExit(11)

if len(folders) != 1:
    print(
        "Nextcloud policy refused: exactly one sync-folder connection is allowed; "
        f"found {len(folders)}.",
        file=sys.stderr,
    )
    raise SystemExit(78)

(account, _folder), fields = next(iter(folders.items()))
if account not in accounts:
    print(
        "Nextcloud policy refused: sync-folder ownership does not match the configured account.",
        file=sys.stderr,
    )
    raise SystemExit(78)

local_path = fields.get("localPath", "")
if not local_path:
    print("Nextcloud policy refused: sync folder has no localPath.", file=sys.stderr)
    raise SystemExit(78)

local_path = os.path.realpath(os.path.expanduser(local_path))
if local_path != canonical_root:
    print(
        "Nextcloud policy refused: configured local root is not the canonical root:\n"
        f"  configured: {local_path}\n"
        f"  required:   {canonical_root}",
        file=sys.stderr,
    )
    raise SystemExit(78)

if not os.path.isdir(canonical_root):
    print(
        "Nextcloud policy refused: configured canonical root is missing; "
        "refusing to recreate sync state implicitly.",
        file=sys.stderr,
    )
    raise SystemExit(78)

if fields.get("targetPath", "") != "/":
    print(
        "Nextcloud policy refused: the single sync connection must target remote root '/'.",
        file=sys.stderr,
    )
    raise SystemExit(78)

if fields.get("_kind") != "FoldersWithPlaceholders":
    print(
        "Nextcloud policy refused: classic sync folders are not allowed; "
        "GjallarOS requires an OpenVFS placeholder folder.",
        file=sys.stderr,
    )
    raise SystemExit(78)

if fields.get("virtualFilesMode", "").strip().lower() != "openvfs":
    print(
        "Nextcloud policy refused: virtualFilesMode must be 'openvfs'.",
        file=sys.stderr,
    )
    raise SystemExit(78)

PY
  '';

  dolphinPlace = pkgs.writeShellScript "nextcloud-dolphin-place" ''
    exec ${lib.getExe pkgs.python3} ${./dolphin-place.py} ${lib.escapeShellArg dolphinNextcloudIcons.places}
  '';

  enrollmentLauncher = pkgs.writeShellScriptBin "nextcloud-enroll" ''
    config="''${XDG_CONFIG_HOME:-$HOME/.config}/Nextcloud/nextcloud.cfg"
    root="$HOME/Nextcloud"
    logdir="''${XDG_STATE_HOME:-$HOME/.local/state}/nextcloud"

    # A running client takes over a second start's options. Start options
    # would hide a plain launch (no window) and restart its logging, and the
    # policies already ran for it.
    if ${lib.getExe' pkgs.systemd "busctl"} --user status com.nextcloud.desktopclient.nextcloud >/dev/null 2>&1; then
      exec ${lib.getExe client} "$@"
    fi

    ${pkgs.coreutils}/bin/install -d -m 0700 "$logdir"
    ${nextcloudConfigPolicy} "$config"

    policy_status=0
    ${nextcloudFolderPolicy} "$config" "$root" || policy_status=$?

    case "$policy_status" in
      0)
        ${dolphinPlace} || true
        exec ${lib.getExe client} --logdir "$logdir" --logflush "$@"
        ;;
      10)
        ;;
      11)
        echo "Nextcloud policy refused: account setup is incomplete; remove the stale local account state and re-enroll."
        exit 78
        ;;
      *)
        exit "$policy_status"
        ;;
    esac

    ${pkgs.coreutils}/bin/install -d -m 0700 "$root"
    ${dolphinPlace} || true

    if [ "$policy_status" -eq 10 ]; then
      if ${pkgs.util-linux}/bin/mountpoint -q "$root"; then
        echo "Nextcloud enrollment refused: $root is still mounted from an earlier session. Log out and back in, then try again." >&2
        exit 78
      fi

      # No account and an empty folder: a sync-root claim left by an
      # earlier account is stale and would block the new one.
      ${lib.getExe pkgs.python3} - "$root" <<'PY' || exit 78
import errno, os, sys

root = sys.argv[1]
try:
    os.removexattr(root, "user.openvfs.registration")
except OSError as exc:
    if exc.errno not in (errno.ENODATA, errno.ENOTSUP):
        print(
            f"Nextcloud enrollment refused: cannot clear the old sync-root claim on {root}: {exc}",
            file=sys.stderr,
        )
        raise SystemExit(78)
else:
    print(f"Nextcloud: cleared an old sync-root claim on {root}.")
PY

      auth_session="$$-$RANDOM-$RANDOM"
      export NEXTCLOUD_AUTH_SESSION="$auth_session"
      export NEXTCLOUD_BROWSER_OPENER=${nextcloudBrowserOpener}
      export NEXTCLOUD_BROWSER_CLOSER=${nextcloudBrowserCloser}

      if [ -n "''${HYPRLAND_INSTANCE_SIGNATURE:-}" ]; then
        workspace="$(${lib.getExe' pkgs.hyprland "hyprctl"} activeworkspace -j 2>/dev/null \
          | ${lib.getExe pkgs.python3} -c '
import json, sys
try:
    workspace = json.load(sys.stdin)
except Exception:
    raise SystemExit(0)
wid = workspace.get("id")
if isinstance(wid, int) and wid > 0:
    print(wid)
' 2>/dev/null || true)"
        if [ -n "$workspace" ]; then
          export NEXTCLOUD_WORKSPACE="$workspace"
        fi
      fi

      override_status=0
      ${lib.getExe client} \
        --overrideserverurl ${lib.escapeShellArg host} \
        --overridelocaldir "$root" \
        || override_status=$?

      if [ "$override_status" -ne 0 ]; then
        echo "Nextcloud enrollment defaults could not be written." >&2
        exit "$override_status"
      fi

      exec ${lib.getExe client} --logdir "$logdir" --logflush "$@"
    fi
  '';

  managedClient = pkgs.symlinkJoin {
    name = "nextcloud-client-managed";
    paths = [
      client
      openvfs
      pkgs.fuse3
    ];
    nativeBuildInputs = [ pkgs.makeWrapper ];

    postBuild = ''
      rm -f "$out/bin/nextcloud"
      makeWrapper \
        ${enrollmentLauncher}/bin/nextcloud-enroll \
        "$out/bin/nextcloud" \
        --prefix XDG_CONFIG_DIRS : "$out/etc/xdg"

      rm -f "$out"/etc/xdg/autostart/*nextcloud*.desktop

      desktop="$out/share/applications/com.nextcloud.desktopclient.nextcloud.desktop"

      if [ -L "$desktop" ]; then
        source="$(${pkgs.coreutils}/bin/readlink -f "$desktop")"
        rm "$desktop"
        cp "$source" "$desktop"
        chmod u+w "$desktop"
      fi

      # The menu entry must start the wrapper, or enrollment is skipped.
      if [ ! -f "$desktop" ]; then
        echo "nextcloud: $desktop is missing" >&2
        exit 1
      fi
      sed -i \
        "0,/^Exec=/{s|^Exec=.*|Exec=$out/bin/nextcloud|}" \
        "$desktop"
      if ! grep -qx "Exec=$out/bin/nextcloud" "$desktop"; then
        echo "nextcloud: no Exec line in $desktop" >&2
        exit 1
      fi
    '';
  };

  syncRunner = pkgs.writeShellScript "nextcloud-sync" ''
    config="''${XDG_CONFIG_HOME:-$HOME/.config}/Nextcloud/nextcloud.cfg"
    root="$HOME/Nextcloud"

    ${nextcloudConfigPolicy} "$config"

    policy_status=0
    ${nextcloudFolderPolicy} "$config" "$root" || policy_status=$?

    case "$policy_status" in
      0)
        ;;
      10)
        echo "Nextcloud account setup is incomplete; automatic enrollment is disabled."
        echo "Run nextcloud-enroll to configure it."
        exit 0
        ;;
      11)
        echo "Nextcloud folder setup is incomplete; automatic sync remains disabled until a clean VFS enrollment is completed."
        exit 0
        ;;
      *)
        exit "$policy_status"
        ;;
    esac

    ${dolphinPlace} || true

    exec ${managedClient}/bin/nextcloud --background
  '';
in
lib.mkMerge [
{
  warnings = lib.optional (enable && !config.gjallar.apps.nextcloud.enable)
    "nextcloudEnable is on, but \"nextcloud\" is not in apps, so the Nextcloud client is not installed. Add \"nextcloud\" to apps, or turn nextcloudEnable off.";
}

(lib.mkIf config.gjallar.apps.nextcloud.enable {
  assertions = lib.optionals enable [
    {
      assertion = host != "";
      message = "Nextcloud integration requires nextcloudHost.";
    }
    {
      assertion = lib.hasPrefix "https://" host;
      message = "Nextcloud integration requires an HTTPS nextcloudHost.";
    }
  ];

  home.packages = lib.optionals enable [
    managedClient
    enrollmentLauncher
  ];

  wayland.windowManager.hyprland.settings.windowrule = lib.optionals enable [
    "match:class ^(${loginClass})$, float on, center on, size (monitor_w*0.5) (monitor_h*0.75)"
    "match:class ^([Nn]extcloud|com\\.nextcloud\\.desktopclient\\.nextcloud)$, focus_on_activate on"
  ];


  # Clients before 164bc3f3 wrote their own autostart entry on first start;
  # the session starts the client. Remove those entries once per user, then
  # leave the folder alone so a later choice in the client stays (wiki 752
  # Q3). Drop this migration once every install has run it.
  home.activation.nextcloudAutostartOwnership = lib.mkIf enable (
    lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      marker=${lib.escapeShellArg "${config.xdg.stateHome}/gjallar/nextcloud-autostart-migrated"}
      if [ ! -e "$marker" ]; then
        for entry in \
          "$HOME/.config/autostart/Nextcloud.desktop" \
          "$HOME/.config/autostart/com.nextcloud.desktopclient.nextcloud.desktop"
        do
          if [ -e "$entry" ]; then
            run ${pkgs.coreutils}/bin/rm -f "$entry"
            echo "GjallarOS: removed the old Nextcloud autostart entry $entry; the session starts the client"
          fi
        done
        run ${pkgs.coreutils}/bin/mkdir -p "$(${pkgs.coreutils}/bin/dirname "$marker")"
        run ${pkgs.coreutils}/bin/touch "$marker"
      fi
    ''
  );

  home.activation.dolphinNextcloudPlace = lib.mkIf enable (
    lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      run ${dolphinPlace} || true
    ''
  );

  systemd.user.services.nextcloud-sync = lib.mkIf enable {
    Unit = {
      Description = "Nextcloud synchronization";
      After = [ "graphical-session.target" ];
      PartOf = [ "graphical-session.target" ];
    };

    Service = {
      Type = "simple";
      ExecStart = syncRunner;

      Restart = "no";

    };

    Install.WantedBy = [
      "graphical-session.target"
    ];
  };

})
]
