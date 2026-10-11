# OpenCode with the gjallarCode logo. apps/ai/nixos.nix and the flake package
# gjallar-opencode both import this file with the system pkgs, so the CI cache
# builds the same derivation that machines install.
{ pkgs }:
pkgs.opencode.overrideAttrs (old: {
  nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [ pkgs.python3 ];

  postPatch = (old.postPatch or "") + ''
          python3 - <<'LOGOPATCH'
    from pathlib import Path
    import re

    candidates = [
        Path(
            "packages/opencode/src/cli/cmd/tui/"
            "routes/home.tsx"
        ),
        Path(
            "packages/opencode/src/cli/cmd/tui/"
            "routes/home/index.tsx"
        ),
    ]

    target = next(
        (p for p in candidates if p.exists()),
        None,
    )

    if target is None:
        raise SystemExit(
            "gjallarCode logo patch: "
            "OpenCode home source not found"
        )

    source = target.read_text()

    logo_lines = [
        "   ____      _       _ _              ____          _",
        "  / ___|    (_) __ _| | | __ _ _ __ / ___|___   __| | ___",
        " | |  _     | |/ _` | | |/ _` | '__| |   / _ \\ / _` |/ _ \\",
        " | |_| |    | | (_| | | | (_| | |  | |__| (_) | (_| |  __/",
        "  \\____|   _/ |\\__,_|_|_|\\__,_|_|   \\____\\___/ \\__,_|\\___|",
        "          |__/",
    ]

    import json

    logo_text = "\n" + "\n".join(logo_lines) + "\n"

    replacement = (
        "<text>{"
        + json.dumps(logo_text)
        + "}</text>"
    )

    patched, count = re.subn(
        r"<Logo(?:\s+[^>]*)?\s*/>",
        lambda _match: replacement,
        source,
        count=1,
    )

    if count != 1:
        raise SystemExit(
            "gjallarCode logo patch: "
            f"expected one Logo element, found {count}"
        )

    patched = re.sub(
        r'(?m)^import\s+\{\s*Logo\s*\}\s+from\s+'
        r'["\'][^"\']*logo["\'];?\s*\n',
        "",
        patched,
        count=1,
    )

    patched = re.sub(
        r'(?m)^import\s+Logo\s+from\s+'
        r'["\'][^"\']*logo["\'];?\s*\n',
        "",
        patched,
        count=1,
    )

    target.write_text(patched)

    verified = target.read_text()

    if "<Logo" in verified:
        raise SystemExit(
            "gjallarCode logo patch verification failed: "
            "original Logo element still present"
        )

    if "<text>{" not in verified:
        raise SystemExit(
            "gjallarCode logo patch verification failed: "
            "ASCII text element missing"
        )

    print("gjallarCode ASCII logo patch verified")

    print(
        "gjallarCode logo patched:",
        target,
    )
    LOGOPATCH
  '';
})
