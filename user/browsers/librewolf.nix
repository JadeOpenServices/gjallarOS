{
  config,
  lib,
  pkgs,
  ...
}:
let
  profileName = "Default";

  # Copies the LibreWolf profile into the Firefox profile. LibreWolf is only
  # read; the Firefox profile is saved to a tarball first.
  import' = pkgs.writers.writePython3Bin "gjallar-librewolf-import" {
    flakeIgnore = [ "E501" ];
  } (builtins.readFile ./librewolf-import.py);
in
{
  home.packages = lib.mkIf config.programs.firefox.enable [
    import'
    (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-librewolf-import" ''
      #compdef gjallar-librewolf-import
      _arguments \
        '--dry-run[list the files without copying]' \
        '--once[import into a new Firefox profile, or add hand-installed add-ons once to an imported one]' \
        '--extensions[copy only the add-ons installed by hand, with their data]' \
        '--src[LibreWolf profile]:directory:_files -/' \
        '--dst[Firefox profile]:directory:_files -/'
    '')
  ];

  # Brings bookmarks, logins and history over the first time Firefox and
  # LibreWolf are both installed. Skips a Firefox profile that was already
  # opened, and tries again next time if a browser is running.
  home.activation.gjallarLibrewolfImport = lib.mkIf config.programs.firefox.enable (
    lib.hm.dag.entryAfter [ "linkGeneration" ] ''
      run ${import'}/bin/gjallar-librewolf-import --once \
        || echo "gjallar-librewolf-import failed; run it by hand to see why" >&2
    ''
  );

  stylix.targets.librewolf.profileNames = [ profileName ];
  stylix.targets.librewolf.colorTheme.enable = true;
  programs.librewolf = {
    enable = true;
    settings = {
      "privacy.clearOnShutdown.history" = false;
      "privacy.clearOnShutdown.sessions" = false;
      "privacy.clearOnShutdown.downloads" = false;
      "privacy.clearOnShutdown.cookies" = false;
      "privacy.clearOnShutdown.formdata" = false;
      "privacy.clearOnShutdown.cache" = false;

      "privacy.clearOnShutdown_v2.cookiesAndStorage" = false;
      "privacy.clearOnShutdown_v2.browsingHistoryAndDownloads" = false;
      "privacy.clearOnShutdown_v2.formdata" = false;
      "privacy.clearOnShutdown_v2.cache" = false;
      "privacy.clearOnShutdown_v2.historyFormDataAndDownloads" = false;
      "privacy.clearOnShutdown_v2.siteSettings" = false;
    };
  };

  programs.librewolf.profiles.${profileName} = {
    extensions = {
      force = true;
      packages = with pkgs.nur.repos.rycee.firefox-addons; [
        nextcloud-passwords
        ublock-origin
        vimium
        stylus
      ];
    };
  };
}
