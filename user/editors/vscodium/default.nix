{
  config,
  inputs,
  lib,
  pkgs,
  ...
}:
let
  semanticTheme = import ../../../themes/lib/semantic.nix { inherit config; };
  contrastGuard = import ../../../themes/lib/contrast.nix { inherit pkgs; };

  settingsPath = "${config.xdg.configHome}/VSCodium/User/settings.json";
  settingsFile = config.home.file.${settingsPath};

  # Home Manager's VS Code module always creates its profile activation,
  # even when only the default profile exists. In that case there is no
  # global-storage profile metadata to reconcile.
  namedVscodiumProfiles = builtins.removeAttrs config.programs.vscodium.profiles [ "default" ];

  # Keep Stylix's font integration, but GjallarOS owns application colors.
  stylixVscodiumSettings =
    import "${inputs.stylix}/modules/vscode/templates/settings.nix" config.stylix.fonts;

  fontSettings = builtins.removeAttrs stylixVscodiumSettings [ "workbench.colorTheme" ];

  # This reproduces the existing Stylix palette through the GjallarOS semantic
  # contract. The following contrast-hardening change can therefore happen in
  # one owner without another VSCodium-specific palette.
  fallbackThemeColors = {
    withHashtag = semanticTheme.base16.withHashtag;
  };

  # Noctalia owns the live application palette. These Base16-shaped names
  # are only an adapter for Stylix's VS Code theme artwork.
  liveThemeColors = {
    withHashtag = {
      scheme = "GjallarOS";

      base00 = "{{colors.surface.default.hex}}";
      base01 = "{{colors.surface_variant.default.hex}}";
      base02 = "{{colors.surface_container_high.default.hex}}";
      base03 = "{{colors.outline_variant.default.hex}}";
      base04 = "{{colors.on_surface_variant.default.hex}}";
      base05 = "{{colors.on_surface.default.hex}}";
      base06 = "{{colors.on_surface.default.hex}}";
      base07 = "{{colors.on_surface.default.hex}}";

      base08 = "{{colors.error.default.hex}}";
      base09 = "{{colors.tertiary.default.hex}}";
      base0A = "{{colors.secondary.default.hex}}";
      base0B = "{{colors.secondary.default.hex}}";
      base0C = "{{colors.tertiary.default.hex}}";
      base0D = "{{colors.primary.default.hex}}";
      base0E = "{{colors.tertiary.default.hex}}";
      base0F = "{{colors.error.default.hex}}";
    };
  };

  fallbackTheme = pkgs.writeText "gjallar-vscodium-theme-fallback.json" (
    builtins.toJSON (
      import "${inputs.stylix}/modules/vscode/templates/theme.nix" fallbackThemeColors
    )
  );

  liveThemeTemplate = pkgs.writeText "gjallar-vscodium-theme-template.json" (
    builtins.toJSON (
      import "${inputs.stylix}/modules/vscode/templates/theme.nix" liveThemeColors
    )
  );

  themeManifest = pkgs.writeText "gjallar-vscodium-theme-package.json" (
    builtins.toJSON {
      name = "gjallar-theme";
      displayName = "GjallarOS";
      description = "GjallarOS semantic desktop color theme";
      publisher = "gjallar";
      version = "1.0.0";

      engines.vscode = "*";

      contributes.themes = [
        {
          label = "GjallarOS";
          uiTheme = "vs-dark";
          path = "./themes/gjallar.json";
        }
      ];
    }
  );

in
{
  # Stylix still owns common fonts and icon assets, but not VSCodium colors.
  stylix.targets.vscodium.enable = false;

  # VSCodium discovers extensions here on Linux. Keep the extension scaffold
  # writable so Noctalia can replace only its generated theme JSON live.
  home.activation.vscodiumGjallarTheme = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    extension_root="$HOME/.vscode-oss/extensions/gjallar.gjallar-theme-1.0.0"
    theme_root="$extension_root/themes"

    run ${pkgs.coreutils}/bin/mkdir -p "$theme_root"
    run ${pkgs.coreutils}/bin/install -m 0644 ${themeManifest} "$extension_root/package.json"

    if [ ! -e "$theme_root/gjallar.json" ]; then
      run ${pkgs.coreutils}/bin/install -m 0644 ${fallbackTheme} "$theme_root/gjallar.json"
    fi
  '';

  # Noctalia rerenders this file whenever its active palette changes.
  programs.noctalia.settings.theme.templates.user.vscodium = {
    input_path = "$XDG_CONFIG_HOME/noctalia/templates/vscodium.json";
    output_path = "~/.vscode-oss/extensions/gjallar.gjallar-theme-1.0.0/themes/gjallar.json";
    pre_hook = contrastGuard.preHook;
  };

  xdg.configFile."noctalia/templates/vscodium.json".source = liveThemeTemplate;

  # Keep the generated settings as first-run defaults, not a read-only live file.
  home.file.${settingsPath}.enable = false;
  # The copy is written once and never refreshed, so a store path in it would
  # outlive its garbage collection. Point at /run/current-system/sw instead.
  assertions = [
    {
      assertion =
        !lib.hasInfix builtins.storeDir (
          builtins.unsafeDiscardStringContext (builtins.toJSON config.programs.vscodium.profiles.default.userSettings)
        );
      message = "VSCodium userSettings must not contain ${builtins.storeDir} paths; settings.json is copied once and is not updated.";
    }
  ];
  home.activation.vscodiumWritableSettings =
    lib.hm.dag.entryBetween [ "linkGeneration" ] [ "writeBoundary" ]
      ''
        settings_file=${lib.escapeShellArg settingsPath}
        settings_source=${lib.escapeShellArg (toString settingsFile.source)}

        if [ -L "$settings_file" ]; then
          case "$(${pkgs.coreutils}/bin/readlink -f "$settings_file")" in
            ${builtins.storeDir}/*) settings_source="$settings_file" ;;
            *) settings_source= ;;
          esac
        elif [ -e "$settings_file" ]; then
          settings_source=
        fi

        if [ -n "$settings_source" ]; then
          run ${pkgs.coreutils}/bin/mkdir -p "$(${pkgs.coreutils}/bin/dirname "$settings_file")"
          if [[ ! -v DRY_RUN ]]; then
            settings_tmp="$(${pkgs.coreutils}/bin/mktemp "$settings_file.XXXXXX")"
            if ! ${pkgs.coreutils}/bin/install -m 0600 "$settings_source" "$settings_tmp" \
              || ! ${pkgs.coreutils}/bin/mv -T "$settings_tmp" "$settings_file"; then
              ${pkgs.coreutils}/bin/rm -f "$settings_tmp"
              exit 1
            fi
          fi
        fi
      '';

  # The settings file intentionally remains writable. Ensure only the theme
  # selection is owned by GjallarOS while preserving all unrelated user edits.
  home.activation.vscodiumGjallarThemeSelection =
    lib.hm.dag.entryAfter [ "vscodiumWritableSettings" ] ''
      settings_file=${lib.escapeShellArg "${config.xdg.configHome}/VSCodium/User/settings.json"}

      if [ -f "$settings_file" ]; then
        if ${pkgs.gnugrep}/bin/grep -Eq \
          '^[[:space:]]*"workbench\.colorTheme"[[:space:]]*:[[:space:]]*"GjallarOS"[[:space:]]*,?[[:space:]]*$' \
          "$settings_file"
        then
          :
        else
          theme_count="$(${pkgs.gnugrep}/bin/grep -Ec '"workbench\.colorTheme"[[:space:]]*:' "$settings_file" || true)"

          case "$theme_count" in
            0)
              if ${pkgs.gnugrep}/bin/grep -q '^[[:space:]]*{' "$settings_file"; then
                run ${pkgs.gnused}/bin/sed -i \
                  '0,/^[[:space:]]*{/{s//&\
  "workbench.colorTheme": "GjallarOS",/}' \
                  "$settings_file"
              else
                echo "VSCodium settings file has no JSON object opening" >&2
                exit 1
              fi
              ;;
            1)
              if ${pkgs.gnugrep}/bin/grep -Eq \
                '^[[:space:]]*"workbench\.colorTheme"[[:space:]]*:[[:space:]]*"[^"]*"[[:space:]]*,?[[:space:]]*$' \
                "$settings_file"
              then
                run ${pkgs.gnused}/bin/sed -i -E \
                  's|^([[:space:]]*)"workbench\.colorTheme"[[:space:]]*:[[:space:]]*"[^"]*"|\1"workbench.colorTheme": "GjallarOS"|' \
                  "$settings_file"
              else
                echo "VSCodium colorTheme setting has an unexpected JSON shape" >&2
                exit 1
              fi
              ;;
            *)
              echo "VSCodium settings contain duplicate workbench.colorTheme keys" >&2
              exit 1
              ;;
          esac
        fi
      fi
    '';

  # Avoid starting the upstream jq-based global-storage helper when there
  # are no named profiles. If named profiles are added later, the upstream
  # activation remains authoritative automatically.
  home.activation.vscodiumProfiles = lib.mkIf (namedVscodiumProfiles == { }) (
    lib.mkForce (lib.hm.dag.entryAfter [ "writeBoundary" ] ":")
  );

  programs.vscodium = {
    enable = true;
    profiles.default.extensions = with pkgs.vscode-extensions; [
      golang.go
      ms-python.python
      redhat.vscode-yaml
      tamasfe.even-better-toml
      pkief.material-icon-theme
      eamodio.gitlens
      usernamehw.errorlens
      streetsidesoftware.code-spell-checker
      editorconfig.editorconfig
      hashicorp.terraform
    ];
    profiles.default.userSettings = fontSettings // {
      "workbench.colorTheme" = "GjallarOS";
      "editor.overwrite" = false;
      "search.followSymlinks" = false;
      "search.useIgnoreFiles" = true;
      "files.watcherExclude" = {
        "**/.git/**" = true;
        "**/node_modules/**" = true;
        "**/dist/**" = true;
        "**/build/**" = true;
        "**/target/**" = true;
        "**/out/**" = true;
        "**/.direnv/**" = true;
        "**/.next/**" = true;
        "**/.turbo/**" = true;
        "**/.venv/**" = true;
        "**/result/**" = true;
      };
      "search.exclude" = {
        "**/.git" = true;
        "**/node_modules" = true;
        "**/dist" = true;
        "**/build" = true;
        "**/target" = true;
        "**/out" = true;
        "**/.direnv" = true;
        "**/.next" = true;
        "**/.turbo" = true;
        "**/.venv" = true;
        "**/result" = true;
        "**/.env*" = true;
        "**/*secret*" = true;
        "**/*.key" = true;
      };
      "files.exclude" = {
        "**/.direnv" = true;
        "**/node_modules" = true;
        "**/result" = true;
        "**/.env*" = true;
        "**/*secret*" = true;
        "**/*.key" = true;
        "**/target" = true;
      };
      "git.confirmSync" = false;
      "git.autofetch" = true;
      "git.enableSmartCommit" = true;
      "npm.autoDetect" = "off";
    };
  };
}
