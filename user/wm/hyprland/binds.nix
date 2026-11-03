{
  config,
  pkgs,
  lib,
  hyprlandShellDetails,
  gjallarRun,
  settings,
  ...
}:
let
  shell = hyprlandShellDetails.binds;
  hl = import ./lua.nix { inherit lib; };
  profile = builtins.fromJSON (builtins.readFile ./keybinds.json);

  # Shortcut list (Super+F1). Reads the live binds from Hyprland; gestures
  # come from gjallar.keybindHelp.gestures because Hyprland does not list them.
  keybindsHelp =
    let
      noctalia = config.programs.noctalia;
      helpConfig = pkgs.writeText "gjallar-keybinds.json" (
        builtins.toJSON {
          inherit (config.gjallar.keybindHelp) gestures;
          drags = map (b: {
            modmask = modmask (b.mods or "");
            inherit (b) key;
          }) profile.bindm;
          menu =
            if noctalia.enable then
              [
                (lib.getExe noctalia.package)
                "dmenu"
                "-p"
                "Keyboard shortcuts"
              ]
            else
              [
                (lib.getExe pkgs.fuzzel)
                "--dmenu"
                "--prompt"
                "Keyboard shortcuts: "
              ];
          # Noctalia shows the part after a tab as a second line.
          menuSplitsTab = noctalia.enable;
        }
      );
    in
    pkgs.writers.writePython3Bin "gjallar-keybinds" { flakeIgnore = [ "E501" ]; } (
      builtins.replaceStrings [ "@config@" ] [ "${helpConfig}" ] (builtins.readFile ./keybinds-help.py)
    );

  commands = {
    volumeUp = shell.volumeUp;
    volumeDown = shell.volumeDown;
    volumeMute = shell.volumeMute;

    brightnessUp = shell.brightnessUp;
    brightnessDown = shell.brightnessDown;

    lock = shell.lock;

    launcher = if shell.launcher == "fuzzel" then lib.getExe pkgs.fuzzel else shell.launcher;

    terminal = "${lib.getExe gjallarRun} ${lib.getExe pkgs.ghostty}";

    editor = "${lib.getExe gjallarRun} ${lib.getExe pkgs.${settings.preferredEditor}}";
    browser = "${lib.getExe gjallarRun} ${
      lib.getExe config.programs.${settings.preferredBrowser}.finalPackage
    }";

    fileManager = lib.getExe config._module.args.gjallarFileManager;

    gjallarAI = "gjallar-ai";

    libreoffice = "${lib.getExe gjallarRun} ${lib.getExe pkgs.libreoffice}";
    teams = "${lib.getExe gjallarRun} gjallar-teams";

    screenshot = shell.screenshot;

    micMute = shell.micMute;
    mediaNext = shell.mediaNext;
    mediaPrev = shell.mediaPrev;
    mediaToggle = shell.mediaToggle;

    keybinds = lib.getExe keybindsHelp;
  };

  # Hyprland modmask bits, as `hyprctl binds -j` reports them.
  modBits = {
    SHIFT = 1;
    CTRL = 4;
    ALT = 8;
    SUPER = 64;
  };
  modmask =
    mods:
    lib.foldl' builtins.add 0 (
      map (m: modBits.${m}) (
        builtins.filter (m: m != "") (
          lib.splitString " " (lib.toUpper (builtins.replaceStrings [ "$mod" ] [ "SUPER" ] mods))
        )
      )
    );

  render =
    opts: binding:
    let
      isExec = binding.action == "exec";

      command =
        if builtins.hasAttr binding.command commands then commands.${binding.command} else binding.command;

      args = binding.args or "";
    in
    hl.bind {
      inherit (binding) key;
      inherit opts;
      mods = binding.mods or "";
      description = binding.desc;
      dispatcher =
        if isExec then
          hl.exec "${command}${if args == "" then "" else " ${args}"}"
        else
          hl.dispatcher binding.action args;
    };

  # Commands that exist only when their app is selected.
  appOf = {
    fileManager = "dolphin";
    gjallarAI = "opencode";
    libreoffice = "libreoffice";
    teams = "teams";
  };

  available =
    binding:
    !(binding.action == "exec" && appOf ? ${binding.command or ""})
    || config.gjallar.apps.${appOf.${binding.command}}.enable;

  renderBindm =
    binding:
    hl.bind {
      inherit (binding) key;
      mods = binding.mods or "";
      description = binding.desc;
      dispatcher = hl.mouseDispatcher binding.action;
      opts.mouse = true;
    };

  # The shortcut list shows the description; a comma would end it early.
  allBinds = profile.bindm ++ profile.binde ++ profile.bind;
  badDescs = map (b: "${b.mods or ""}, ${b.key}") (
    builtins.filter (b: (b.desc or "") == "" || lib.hasInfix "," b.desc) allBinds
  );

  # Hyprland matches keys without case and mods in any order, so
  # "$mod, L" and "$mod, l" are the same bind.
  bindId =
    binding:
    let
      mods = builtins.filter (m: m != "") (lib.splitString " " (lib.toUpper (binding.mods or "")));
    in
    "${lib.concatStringsSep " " (lib.sort lib.lessThan mods)}, ${lib.toUpper binding.key}";
  bindIds = map bindId (profile.bind ++ profile.binde);
  duplicateBinds = lib.unique (builtins.filter (id: lib.count (x: x == id) bindIds > 1) bindIds);
in
{
  options.gjallar.keybindHelp.gestures = lib.mkOption {
    type = lib.types.listOf (
      lib.types.submodule {
        options = {
          keys = lib.mkOption { type = lib.types.str; };
          description = lib.mkOption { type = lib.types.str; };
        };
      }
    );
    default = [ ];
    internal = true;
    description = "Gestures listed by gjallar-keybinds next to the shortcuts.";
  };

  config.assertions = [
    {
      assertion = badDescs == [ ];
      message = "keybinds.json: every bind needs a \"desc\" without commas: ${lib.concatStringsSep "; " badDescs}";
    }
    {
      assertion = duplicateBinds == [ ];
      message = "keybinds.json binds the same key twice: ${lib.concatStringsSep "; " duplicateBinds}";
    }
  ];

  config.wayland.windowManager.hyprland.settings = {
    # The description is what `hyprctl binds` reports and the shortcut list
    # shows. "$mod" in keybinds.json is Super.
    bind =
      map renderBindm profile.bindm
      ++ map (render { repeating = true; }) (builtins.filter available profile.binde)
      ++ map (render { }) (builtins.filter available profile.bind);
  };

  config.home.packages = [
    keybindsHelp
    (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-keybinds" ''
      #compdef gjallar-keybinds
      _arguments '--print[write the list to the terminal]' '(-h --help)'{-h,--help}'[show usage]'
    '')
  ];

  config.xdg.desktopEntries.gjallar-keybinds = {
    name = "Keyboard shortcuts";
    comment = "All keyboard shortcuts and gestures";
    exec = lib.getExe keybindsHelp;
    icon = "preferences-desktop-keyboard-shortcuts";
    categories = [ "Utility" ];
  };
}
