"""Show every Hyprland shortcut and gesture with what it does.

Reads the live binds from `hyprctl binds -j`, so the list always matches the
running session, including binds the user added. The gestures come from the
GjallarOS config, because Hyprland does not report them.
"""
import json
import re
import shutil
import subprocess
import sys

CONFIG = "@config@"

# Hyprland modmask bits, in the order people say them.
MODS = [(64, "Super"), (4, "Ctrl"), (8, "Alt"), (1, "Shift"),
        (2, "Caps"), (16, "Mod2"), (32, "Mod3"), (128, "Mod5")]

KEYS = {
    "mouse:272": "Left click",
    "mouse:273": "Right click",
    "mouse:274": "Middle click",
    "mouse_down": "Scroll down",
    "mouse_up": "Scroll up",
    "mouse_left": "Scroll left",
    "mouse_right": "Scroll right",
    "code:382": "Keyboard key",
    "XF86AudioRaiseVolume": "Volume up key",
    "XF86AudioLowerVolume": "Volume down key",
    "XF86AudioMute": "Mute key",
    "XF86AudioMicMute": "Mic mute key",
    "XF86AudioNext": "Next track key",
    "XF86AudioPrev": "Previous track key",
    "XF86AudioPlay": "Play key",
    "XF86MonBrightnessUp": "Brightness up key",
    "XF86MonBrightnessDown": "Brightness down key",
    "XF86ScreenSaver": "Lock key",
    "Return": "Enter",
    "Escape": "Esc",
    "Left": "Left arrow",
    "Right": "Right arrow",
    "Up": "Up arrow",
    "Down": "Down arrow",
}

MOUSE_DRAG = {
    "mouse:272": "Left drag",
    "mouse:273": "Right drag",
    "mouse:274": "Middle drag",
}


def key_name(bind):
    key = bind["key"]
    if bind.get("mouse") and key in MOUSE_DRAG:
        return MOUSE_DRAG[key]
    if key in KEYS:
        return KEYS[key]
    if key.startswith("XF86"):
        return re.sub(r"(?<=[a-z])(?=[A-Z])", " ", key[4:]) + " key"
    return key.upper() if len(key) == 1 else key.capitalize()


def keys_text(bind):
    mods = [name for bit, name in MODS if bind["modmask"] & bit]
    return " + ".join(mods + [key_name(bind)])


def what_text(bind):
    if bind.get("has_description") and bind.get("description"):
        return bind["description"]
    # A bind the user added without a description: show what it runs, with
    # store paths cut down to the program name. A Lua bind reports only a
    # reference number, which tells nobody anything.
    if bind.get("dispatcher") == "__lua":
        return "No description"
    arg = re.sub(r"/nix/store/[^ ]*/bin/", "", bind.get("arg", ""))
    return f"{bind['dispatcher']} {arg}".strip()


def merge_numbered(rows):
    """Turn "Super + 1: Go to workspace 1" ... "Super + 0: Go to workspace 10"
    into one row."""
    groups = []
    for keys, what in rows:
        k = re.fullmatch(r"(.*?)(\d)", keys)
        m = re.fullmatch(r"(.*) (\d+)", what)
        if not (k and m):
            groups.append((keys, what))
            continue
        last = groups[-1] if groups else None
        if isinstance(last, list) and last[0] == k.group(1) and last[3] == m.group(1):
            last[2], last[5] = k.group(2), m.group(2)
            continue
        groups.append([k.group(1), k.group(2), k.group(2),
                       m.group(1), m.group(2), m.group(2)])
    out = []
    for g in groups:
        if isinstance(g, tuple):
            out.append(g)
        elif g[1] == g[2]:
            out.append((g[0] + g[1], f"{g[3]} {g[4]}"))
        else:
            out.append((f"{g[0]}{g[1]} … {g[2]}", f"{g[3]} {g[4]} to {g[5]}"))
    return out


def merge_same(rows):
    """One row per action: "Super + D or Super + Shift + A"."""
    order, keys = [], {}
    for k, what in rows:
        if what not in keys:
            order.append(what)
            keys[what] = []
        if k not in keys[what]:
            keys[what].append(k)
    return [("  or  ".join(keys[what]), what) for what in order]


def shortcut_rows(drags):
    raw = subprocess.run(["hyprctl", "binds", "-j"], check=True,
                         capture_output=True, text=True).stdout
    rows = []
    for bind in json.loads(raw):
        # Switch binds (laptop lid, tablet hinge) are not shortcuts.
        if bind["key"].startswith("switch:") or bind.get("submap"):
            continue
        # Lua binds do not report whether they drag, so the config lists them.
        if (bind["modmask"], bind["key"]) in drags:
            bind["mouse"] = True
        rows.append((keys_text(bind), what_text(bind)))
    return merge_same(merge_numbered(rows))


def main():
    with open(CONFIG, encoding="utf-8") as f:
        config = json.load(f)

    args = sys.argv[1:]
    if args and args[0] in ("-h", "--help"):
        print("usage: gjallar-keybinds [--print]\n\n"
              "Shows all keyboard shortcuts and gestures. --print writes the\n"
              "list to the terminal instead of opening a menu.")
        return 0
    if args and args[0] != "--print":
        print(f"gjallar-keybinds: unknown option {args[0]}", file=sys.stderr)
        return 2

    drags = {(d["modmask"], d["key"]) for d in config["drags"]}
    rows = shortcut_rows(drags)
    rows += [(g["keys"], g["description"]) for g in config["gestures"]]

    if args:
        width = max(len(k) for k, _ in rows)
        if width + 2 + max(len(w) for _, w in rows) <= shutil.get_terminal_size().columns:
            for k, what in rows:
                print(f"{k:<{width}}  {what}")
        else:
            # Too narrow for two columns: description under the keys.
            for k, what in rows:
                print(f"{k}\n    {what}")
        return 0

    sep = "\t" if config["menuSplitsTab"] else "   —   "
    text = "".join(f"{k}{sep}{what}\n" for k, what in rows)
    # Picking a row does nothing; the menu is only for reading and searching.
    subprocess.run(config["menu"], input=text, text=True,
                   stdout=subprocess.DEVNULL, check=False)
    return 0


sys.exit(main())
