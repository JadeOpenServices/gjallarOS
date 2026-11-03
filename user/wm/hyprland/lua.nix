{ lib }:

# Helpers for the Hyprland Lua config. Settings render as hl.<name>(...);
# these build the values that need a Lua expression rather than data.
let
  toLua = lib.generators.toLua { };
  inline = lib.generators.mkLuaInline;

  # "$mod SHIFT" + "Q" -> "SUPER + SHIFT + Q"
  keyString =
    mods: key:
    let
      names = builtins.filter (m: m != "") (
        lib.splitString " " (builtins.replaceStrings [ "$mod" ] [ "SUPER" ] mods)
      );
    in
    lib.concatStringsSep " + " (names ++ [ key ]);

  exec = command: inline "hl.dsp.exec_cmd(${toLua command})";

  call = name: args: inline "hl.dsp.${name}(${if args == null then "" else toLua args})";

  # Legacy dispatcher name and argument (as keybinds.json writes them) to the
  # Lua dispatcher. An unknown name fails the build instead of a dead key.
  dispatcher =
    action: arg:
    let
      workspace = n: if builtins.match "[0-9]+" n != null then lib.toInt n else n;
      xy =
        let
          parts = builtins.filter (p: p != "") (lib.splitString " " arg);
        in
        {
          x = lib.toInt (builtins.elemAt parts 0);
          y = lib.toInt (builtins.elemAt parts 1);
          relative = true;
        };
      table = {
        killactive = call "window.close" null;
        pseudo = call "window.pseudo" null;
        centerwindow = call "window.center" null;
        togglefloating = call "window.float" { action = "toggle"; };
        fullscreen = call "window.fullscreen" {
          mode = if arg == "1" then "maximized" else "fullscreen";
        };
        movefocus = call "focus" { direction = arg; };
        movewindow = call "window.move" { direction = arg; };
        movetoworkspace = call "window.move" { workspace = workspace arg; };
        movetoworkspacesilent = call "window.move" {
          workspace = workspace arg;
          follow = false;
        };
        workspace = call "focus" { workspace = workspace arg; };
        focusworkspaceoncurrentmonitor = call "focus" {
          workspace = workspace arg;
          on_current_monitor = true;
        };
        focusmonitor = call "focus" { monitor = arg; };
        togglespecialworkspace = inline "hl.dsp.workspace.toggle_special(${
          toLua (if arg == "" then "special" else arg)
        })";
        layoutmsg = inline "hl.dsp.layout(${toLua arg})";
        resizeactive = call "window.resize" xy;
        exit = call "exit" null;
      };
    in
    table.${action} or (throw "user/wm/hyprland/lua.nix: no Lua dispatcher for \"${action}\"");

  # Mouse binds: drag moves, resize resizes; both follow the pointer.
  mouseDispatcher =
    action:
    {
      movewindow = call "window.drag" null;
      resizewindow = call "window.resize" null;
    }
    .${action} or (throw "user/wm/hyprland/lua.nix: no Lua mouse dispatcher for \"${action}\"");

  # One hl.bind(keys, dispatcher, opts) call as a settings.bind entry.
  bind =
    {
      mods ? "",
      key,
      dispatcher,
      description,
      opts ? { },
    }:
    {
      _args = [
        (keyString mods key)
        dispatcher
        (opts // { inherit description; })
      ];
    };
in
{
  inherit
    toLua
    inline
    keyString
    exec
    dispatcher
    mouseDispatcher
    bind
    ;
}
