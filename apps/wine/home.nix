{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  winePackage = pkgs.wineWow64Packages.staging;

  wineInit = pkgs.writeShellScriptBin "wine-win11-init" ''
    set -euo pipefail

    export WINEPREFIX="''${WINEPREFIX:-$HOME/.local/share/wine/win11}"
    export WINEARCH=win64
    export WINEDEBUG="-all"

    unset WINEDLLOVERRIDES

    install -d -m 0700 "$WINEPREFIX"

    # Fails when no wineserver runs, which is the usual case.
    ${winePackage}/bin/wineserver -k || true

    ${winePackage}/bin/wineboot -u
    ${winePackage}/bin/wineserver -w || true

    # A failed step leaves the prefix unmarked, so the next start tries again;
    # winetricks skips the verbs it already installed. Marking it anyway kept a
    # prefix set up offline incomplete for good.
    failed=()
    reg_add() {
      ${winePackage}/bin/wine reg add "$1" /v "$2" /t "$3" /d "$4" /f ||
        failed+=("reg $2")
    }
    tricks() {
      ${pkgs.winetricks}/bin/winetricks -q "$@" || failed+=("winetricks $*")
      ${winePackage}/bin/wineserver -w || true
    }

    reg_add 'HKCU\Software\Wine\X11 Driver' ClientSideWithRender REG_SZ N
    reg_add 'HKCU\Software\Wine\X11 Driver' DirectColor REG_SZ Y
    reg_add 'HKCU\Software\Wine\X11 Driver' Decorated REG_SZ Y
    reg_add 'HKCU\Software\Wine\X11 Driver' Managed REG_SZ Y
    reg_add 'HKCU\Software\Wine\Direct3D' csmt REG_DWORD 1
    reg_add 'HKCU\Software\Wine\Direct3D' MaxVersionGL REG_DWORD 40005
    reg_add 'HKCU\Software\Microsoft\Avalon.Graphics' DisableHWAcceleration REG_DWORD 1

    tricks corefonts comctl32 gdiplus vcrun2022
    tricks dxvk vkd3d d3dcompiler_43 d3dcompiler_47 d3dx9 dcomp
    tricks dotnet48 dotnetdesktop6 dotnetdesktop8

    if ((''${#failed[@]})); then
      echo "wine-win11-init: incomplete (''${failed[*]}); the next start tries again" >&2
      exit 1
    fi

    touch "$WINEPREFIX/.gjallar-wine-initialized"
  '';

  wineWin11 = pkgs.writeShellScriptBin "wine-win11" ''
    set -euo pipefail

    prefix="''${WINEPREFIX:-$HOME/.local/share/wine/win11}"

    export WINEPREFIX="$prefix"
    export WINEARCH=win64
    export WINEESYNC=1
    export WINEFSYNC=1
    export DXVK_LOG_LEVEL=none
    export DXVK_ASYNC=0
    export DXVK_ENABLE_NVAPI=0
    export DXVK_FRAME_RATE=0
    export WINE_FULLSCREEN_FSR=0
    export DXVK_STATE_CACHE=1
    export DXVK_STATE_CACHE_PATH="''${XDG_CACHE_HOME:-$HOME/.cache}/dxvk"
    export STAGING_SHARED_MEMORY=1
    export WINE_LARGE_ADDRESS_AWARE=1
    export VKD3D_FEATURE_LEVEL=12_1
    export PULSE_LATENCY_MSEC=60
    export WPF_DISABLE_HARDWARE_ACCELERATION=1
    export WINEDEBUG="-fixme"
    export WINEDLLOVERRIDES="winhttp=n,b;d3dcompiler_47=n,b;d3dcompiler_43=n,b;gdiplus=n,b;dxgi=n,b;d3d11=n,b;d3d10core=n,b;d3d9=n,b;dcomp=n,b"

    mkdir -p "$DXVK_STATE_CACHE_PATH"

    if [[ ! -d "$prefix" ]] ||
       [[ ! -f "$prefix/.gjallar-wine-initialized" ]]; then
      mkdir -p "$(dirname "$prefix")"

      exec 9>"$prefix.gjallar-init.lock"
      ${pkgs.util-linux}/bin/flock 9

      if [[ ! -f "$prefix/.gjallar-wine-initialized" ]]; then
        ${wineInit}/bin/wine-win11-init ||
          echo "wine-win11: starting with an incomplete Wine setup" >&2
      fi
    fi

    if [[ "$#" -gt 0 ]]; then
      exec ${winePackage}/bin/wine "$@"
    fi
  '';

  wineDesktop = pkgs.makeDesktopItem {
    name = "wine-win11";
    desktopName = "Wine Windows 11 Wrapper";
    comment = "Run Windows executables through GjallarOS Wine";
    exec = "${wineWin11}/bin/wine-win11 %f";
    terminal = false;

    mimeTypes = [
      "application/x-ms-dos-executable"
      "application/x-msi"
      "application/x-ms-shortcut"
      "application/x-wine-extension-msp"
    ];

    categories = [ "Utility" ];
  };

  fexPackages =
    if settings.system == "aarch64-linux" &&
       builtins.hasAttr "fex" pkgs then
      [ pkgs.fex ]
    else if settings.system == "aarch64-linux" &&
            builtins.hasAttr "fex-emu" pkgs then
      [ pkgs.fex-emu ]
    else
      [ ];
in
lib.mkIf config.gjallar.apps.wine.enable {
  home.packages =
    [
      winePackage
      wineInit
      wineWin11
      wineDesktop
      (pkgs.writeTextDir "share/zsh/site-functions/_wine-win11" ''
        #compdef wine-win11
        _arguments '1:Windows program:_files -g "*.(exe|msi|bat|lnk|EXE|MSI|BAT|LNK)"' '*::argument:_files'
      '')

      pkgs.winetricks
      pkgs.dxvk
      pkgs.vkd3d
      pkgs.cabextract
      pkgs.p7zip
    ]
    ++ fexPackages;

  xdg.mimeApps = {
    enable = true;

    defaultApplications = {
      "application/x-ms-dos-executable" = [ "wine-win11.desktop" ];
      "application/x-msi" = [ "wine-win11.desktop" ];
      "application/x-ms-shortcut" = [ "wine-win11.desktop" ];
      "application/x-wine-extension-msp" = [ "wine-win11.desktop" ];
    };
  };
}
