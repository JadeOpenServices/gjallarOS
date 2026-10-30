{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  winePackage = pkgs._wine;
  sandboxLauncher = pkgs.callPackage ../../pkgs/gjallar-wine { inherit wineInit; };

  # The winetricks vkd3d verb asks the GitHub API for the latest release, which
  # fails offline and under its rate limit. The hash is the digest GitHub lists
  # for the v3.0.1 asset. The verb name has to match the file name.
  vkd3dProtonVerb = pkgs.writeTextDir "share/gjallar/winetricks/vkd3d_proton301.verb" ''
    w_metadata vkd3d_proton301 dlls \
        title="Vulkan-based D3D12 implementation for Linux / Wine (3.0.1)" \
        publisher="Hans-Kristian Arntzen" \
        year="2026" \
        media="download" \
        file1="vkd3d-proton-3.0.1.tar.zst" \
        installed_file1="''${W_SYSTEM32_DLLS_WIN}/d3d12.dll" \
        installed_file2="''${W_SYSTEM32_DLLS_WIN}/d3d12core.dll"

    load_vkd3d_proton301()
    {
        w_download "https://github.com/HansKristian-Work/vkd3d-proton/releases/download/v3.0.1/vkd3d-proton-3.0.1.tar.zst" 3cf2315522af5e43605ef6d3c41dad91387040bf97199934f3f7ab76caaa2f0c
        helper_vkd3d_proton "''${file1}"
    }
  '';

  # The setup run's own winetricks (Q74). In unattended mode winetricks keeps
  # going after a SHA256 mismatch when it has no dialog to ask, and accepts
  # files with no checksum. Both stop the step here instead. The Wayback
  # Machine retry stays, since the hash still has to match. aka.ms always
  # points at the newest VC runtime; the versioned URL it redirects to is
  # named by the hash winetricks pins. A winetricks update that changes
  # these lines fails the build until the pins are checked again.
  winetricks = pkgs.winetricks.overrideAttrs (old: {
    postPatch = old.postPatch + ''
      substituteInPlace src/winetricks \
        --replace-fail 'w_askpermission "SHA256 mismatch!' \
          'w_die "SHA256 mismatch for ''${_W_url}"; w_askpermission "SHA256 mismatch!' \
        --replace-fail '    w_get_shatype "''${_W_sum}"' \
          '    w_get_shatype "''${_W_sum}"; test -n "''${_W_sum}" || w_die "No checksum for ''${_W_url}"' \
        --replace-fail \
          'https://aka.ms/vs/17/release/vc_redist.x86.exe 0c09f2611660441084ce0df425c51c11e147e6447963c3690f97e0b25c55ed64' \
          'https://download.visualstudio.microsoft.com/download/pr/990c720a-46ce-4dc7-87b9-c2eb615d34b9/0C09F2611660441084CE0DF425C51C11E147E6447963C3690F97E0B25C55ED64/VC_redist.x86.exe 0c09f2611660441084ce0df425c51c11e147e6447963c3690f97e0b25c55ed64 vc_redist.x86.exe' \
        --replace-fail \
          'https://aka.ms/vs/17/release/vc_redist.x64.exe cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b' \
          'https://download.visualstudio.microsoft.com/download/pr/990c720a-46ce-4dc7-87b9-c2eb615d34b9/CC0FF0EB1DC3F5188AE6300FAEF32BF5BEEBA4BDD6E8E445A9184072096B713B/VC_redist.x64.exe cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b vc_redist.x64.exe'
    '';
  });

  # Runs on the first start, not at rebuild (Q38): the runtimes come from their
  # publishers and stay out of our cache. The desktop launcher has no terminal,
  # so progress goes to a window and the full output to a log.
  wineInit = pkgs.writeShellScriptBin "wine-win11-init" ''
    set -euo pipefail

    export WINEPREFIX="''${WINEPREFIX:-$HOME/.local/share/wine/win11}"
    export WINEARCH=win64
    export WINEDEBUG="-all"
    # winetricks calls wine from PATH and needs unzstd for vkd3d-proton.
    export PATH="${lib.makeBinPath [ winePackage pkgs.zstd ]}:$PATH"
    # bin/wine is a shell wrapper, so winetricks cannot read its arch, takes
    # the prefix for an unknown WoW64 style and runs no wine at all. WINE_BIN
    # is its switch for wrappers.
    export WINE_BIN="${winePackage}/bin/.wine"

    unset WINEDLLOVERRIDES

    state="''${XDG_STATE_HOME:-$HOME/.local/state}/wine-win11"
    log="$state/setup.log"
    install -d -m 0700 "$state" "$WINEPREFIX"

    exec 3>&2 >>"$log" 2>&1
    echo "--- $(date -Is) wine-win11-init"

    gui=
    if [[ ! -t 3 ]] && [[ -n "''${WAYLAND_DISPLAY:-''${DISPLAY:-}}" ]]; then
      exec 4> >(${pkgs.zenity}/bin/zenity --progress --title="Wine setup" \
        --width=560 --no-cancel --auto-close --percentage=0 || true)
      gui=1
    fi

    step=0
    steps=12
    say() {
      echo "wine-win11 setup: $1"
      echo "wine-win11 setup: $1" >&3 || true
      # A subshell, so a closed window ends only the write, not the setup.
      if [[ -n "$gui" ]]; then
        ( printf '%s\n# %s\n' "$((step * 100 / steps))" "$1" >&4 ) 2>/dev/null || true
      fi
    }
    finish() {
      step=$steps
      say "$1"
      if [[ -n "$gui" ]]; then
        exec 4>&-
        ${pkgs.libnotify}/bin/notify-send -a Wine "Wine setup" "$1" || true
      fi
    }

    if [[ -f "$WINEPREFIX/system.reg" ]] &&
       ${pkgs.procps}/bin/pgrep -u "$(id -u)" -x wineserver >/dev/null; then
      finish "Wine setup is not complete yet, and a Wine program is still open. Setup continues on a start when no Wine program runs. Details: $log"
      exit 1
    fi

    say "First start of Wine: setting up $WINEPREFIX. This happens once and can take 10 to 30 minutes. It downloads about 500 MB of fonts and runtimes (Visual C++, .NET, DirectX parts) from Microsoft, GitHub, SourceForge and files.holarse-linuxgaming.de. Details: $log"

    # A failed step leaves the prefix unmarked, so the next start tries again;
    # winetricks skips the verbs it already installed. Marking it anyway kept a
    # prefix set up offline incomplete for good.
    failed=()
    reg_add() {
      wine reg add "$1" /v "$2" /t "$3" /d "$4" /f || failed+=("reg $2")
    }

    step=1
    say "Creating the Wine environment"
    # Earlier versions ran the winetricks comctl32 verb. It sets comctl32 5.80
    # to native and deletes the common-controls 6 manifests, so the Visual C++
    # and .NET installers cannot open their window (0x583). Without the
    # override, wineboot -u puts the manifests back.
    if grep -q '^"\*comctl32"="native,builtin"' "$WINEPREFIX/user.reg" 2>/dev/null; then
      wine reg delete 'HKCU\Software\Wine\DllOverrides' /v '*comctl32' /f ||
        failed+=("reg *comctl32")
    fi
    wineboot -u || failed+=("wineboot")
    wineserver -w || true

    step=2
    say "Applying display settings"
    reg_add 'HKCU\Software\Wine\X11 Driver' ClientSideWithRender REG_SZ N
    reg_add 'HKCU\Software\Wine\X11 Driver' DirectColor REG_SZ Y
    reg_add 'HKCU\Software\Wine\X11 Driver' Decorated REG_SZ Y
    reg_add 'HKCU\Software\Wine\X11 Driver' Managed REG_SZ Y
    reg_add 'HKCU\Software\Wine\Direct3D' csmt REG_DWORD 1
    reg_add 'HKCU\Software\Wine\Direct3D' MaxVersionGL REG_DWORD 40005
    reg_add 'HKCU\Software\Microsoft\Avalon.Graphics' DisableHWAcceleration REG_DWORD 1

    # DXVK comes from Nix, so no download. setup_dxvk.sh is not used: with a
    # WoW64 Wine it resolves both DLL folders to system32.
    step=3
    say "Installing DXVK ${pkgs.dxvk.version} (no download)"
    windir="$WINEPREFIX/drive_c/windows"
    for dll in d3d8 d3d9 d3d10core d3d11 dxgi; do
      if install -m 0644 "${pkgs.dxvk.bin}/x64/$dll.dll" "$windir/system32/$dll.dll" &&
         install -m 0644 "${pkgs.dxvk.bin}/x32/$dll.dll" "$windir/syswow64/$dll.dll"; then
        reg_add 'HKCU\Software\Wine\DllOverrides' "$dll" REG_SZ native
      else
        failed+=("dxvk $dll")
      fi
    done

    downloads=(
      "corefonts|Core fonts"
      "vcrun2022|Visual C++ 2015-2022 runtime"
      "${vkd3dProtonVerb}/share/gjallar/winetricks/vkd3d_proton301.verb|vkd3d-proton 3.0.1 (Direct3D 12)"
      "d3dcompiler_43|Direct3D shader compiler 43"
      "d3dcompiler_47|Direct3D shader compiler 47"
      "d3dx9|D3DX9"
      "dotnet48|.NET Framework 4.8|the longest step"
      "dotnetdesktop6|.NET 6 desktop runtime"
      "dotnetdesktop8|.NET 8 desktop runtime"
    )

    online=1
    ${pkgs.curl}/bin/curl -fsS --max-time 15 -o /dev/null https://github.com || online=

    for entry in "''${downloads[@]}"; do
      verb="''${entry%%|*}"
      rest="''${entry#*|}"
      label="''${rest%%|*}"
      note=
      [[ "$rest" == *"|"* ]] && note=" (''${rest#*|})"
      step=$((step + 1))
      if [[ -z "$online" ]]; then
        failed+=("$label")
        continue
      fi
      say "Downloading and installing: $label$note"
      ${winetricks}/bin/winetricks -q "$verb" || failed+=("$label")
      wineserver -w || true
    done

    if [[ -z "$online" ]]; then
      finish "No internet connection, so Wine set up only the parts that need no download. The program starts now, but some programs need the missing runtimes. The next start with internet finishes the setup. Details: $log"
      exit 1
    fi
    if ((''${#failed[@]})); then
      missing="$(printf '%s, ' "''${failed[@]}")"
      finish "Wine setup is not complete. Missing: ''${missing%, }. The program starts now, and the next start tries again. Details: $log"
      exit 1
    fi

    touch "$WINEPREFIX/.gjallar-wine-initialized"
    finish "Wine setup is done. Programs start without this step from now on."
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
    export WINEDLLOVERRIDES="winhttp=n,b;d3dcompiler_47=n,b;d3dcompiler_43=n,b;dxgi=n,b;d3d11=n,b;d3d10core=n,b;d3d9=n,b"

    mkdir -p "$DXVK_STATE_CACHE_PATH"

    if [[ ! -d "$prefix" ]] ||
       [[ ! -f "$prefix/.gjallar-wine-initialized" ]]; then
      mkdir -p "$(dirname "$prefix")"

      exec 9>"$prefix.gjallar-init.lock"
      ${pkgs.util-linux}/bin/flock 9

      if [[ ! -f "$prefix/.gjallar-wine-initialized" ]]; then
        ${wineInit}/bin/wine-win11-init 9>&- ||
          echo "wine-win11: starting with an incomplete Wine setup" >&2
      fi
      # Wine and its wineserver would inherit the lock and hold it.
      exec 9>&-
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

      sandboxLauncher
      # hold, lock and stay are the launcher's own steps, so they are not offered.
      (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-wine" ''
        #compdef gjallar-wine
        _gjallar_wine_ids() {
          local -a ids
          ids=(''${HOME}/.local/share/gjallar-wine/apps/*.json(N:t:r))
          _describe -t apps 'Wine app' ids
        }
        _gjallar_wine_installers() {
          _files -g '*.(exe|msi|EXE|MSI)'
        }
        local -a commands
        commands=(
          'add:add an app and run its installer'
          'install:run an installer in an app'
          'program:show or pick the program an app starts'
          'run:start a Wine app in its sandbox'
          'remove:remove an app'
        )
        if (( CURRENT == 2 )); then
          _describe -t commands 'gjallar-wine command' commands
          return
        fi
        case $words[2] in
          add)
            (( CURRENT == 4 )) && _gjallar_wine_installers ;;
          install)
            if (( CURRENT == 3 )); then _gjallar_wine_ids
            elif (( CURRENT == 4 )); then _gjallar_wine_installers; fi ;;
          program)
            (( CURRENT == 3 )) && _gjallar_wine_ids ;;
          run)
            if (( CURRENT == 3 )); then _gjallar_wine_ids; else _files; fi ;;
          remove)
            if (( CURRENT == 3 )); then _gjallar_wine_ids
            elif (( CURRENT == 4 )); then compadd -- --delete-files; fi ;;
        esac
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
