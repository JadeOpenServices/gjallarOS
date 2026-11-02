{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  winePackage = pkgs._wine;
  sandboxLauncher = pkgs.callPackage ../../pkgs/gjallar-wine {
    inherit wineInit winetricks;
    dolphinPlace = pkgs.callPackage ../../pkgs/dolphin-place { };
  };

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

  winetricks = pkgs.callPackage ../../pkgs/gjallar-winetricks { };

  # Sets up the base prefix every Wine app is copied from (Plane 272). It runs
  # when the first app is added, not at rebuild (Q38): the runtimes come from
  # their publishers and stay out of our cache. gjallarctl wine add may run
  # without a terminal, so progress also goes to a window and the full output
  # to a log.
  wineInit = pkgs.writeShellScriptBin "gjallar-wine-init" ''
    set -euo pipefail

    export WINEPREFIX="''${WINEPREFIX:?}"
    export WINEARCH=win64
    export WINEDEBUG="-all"
    # winetricks calls wine from PATH and needs unzstd for vkd3d-proton.
    export PATH="${lib.makeBinPath [ winePackage pkgs.zstd ]}:$PATH"
    # bin/wine is a shell wrapper, so winetricks cannot read its arch, takes
    # the prefix for an unknown WoW64 style and runs no wine at all. WINE_BIN
    # is its switch for wrappers.
    export WINE_BIN="${winePackage}/bin/.wine"

    unset WINEDLLOVERRIDES

    state="''${XDG_STATE_HOME:-$HOME/.local/state}/gjallar-wine"
    log="$state/setup.log"
    install -d -m 0700 "$state" "$WINEPREFIX"

    exec 3>&2 >>"$log" 2>&1
    echo "--- $(date -Is) gjallar-wine-init"

    gui=
    if [[ ! -t 3 ]] && [[ -n "''${WAYLAND_DISPLAY:-''${DISPLAY:-}}" ]]; then
      exec 4> >(${pkgs.zenity}/bin/zenity --progress --title="Wine setup" \
        --width=560 --no-cancel --auto-close --percentage=0 || true)
      gui=1
    fi

    step=0
    steps=12
    say() {
      echo "Wine setup: $1"
      echo "Wine setup: $1" >&3 || true
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
    # A .NET step run by winetricks before PR #2533 left OnlyUseLatestCLR
    # out for 32-bit programs, and wineboot -u then never ends (see the
    # winetricks override above). A value already there stays as it is.
    if [[ -f "$WINEPREFIX/system.reg" ]] &&
       ${pkgs.gawk}/bin/awk '
         /^\[/ { s = tolower($1) }
         tolower($0) ~ /^"onlyuselatestclr"=/ {
           if (s == "[software\\\\microsoft\\\\.netframework]") a = (tolower($0) == "\"onlyuselatestclr\"=dword:00000001")
           if (s == "[software\\\\wow6432node\\\\microsoft\\\\.netframework]") b = 1
         }
         END { exit !(a && !b) }' "$WINEPREFIX/system.reg"; then
      reg_add 'HKLM\Software\Wow6432Node\Microsoft\.NETFramework' OnlyUseLatestCLR REG_DWORD 1
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

  # A double click on an installer opens the settings' "New Wine app" with
  # it filled in. Hidden from the menu: it needs a file.
  setupDesktop = pkgs.makeDesktopItem {
    name = "gjallar-wine-setup";
    desktopName = "New Wine app";
    comment = "Set up a Wine app with this installer";
    exec = "gjallar-settings wine-setup %f";
    icon = "wine";
    terminal = false;
    noDisplay = true;
    mimeTypes = [
      "application/x-ms-dos-executable"
      "application/x-msi"
    ];
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
      sandboxLauncher
      setupDesktop
    ]
    ++ fexPackages;

  xdg.mimeApps = {
    enable = true;

    defaultApplications = {
      "application/x-ms-dos-executable" = [ "gjallar-wine-setup.desktop" ];
      "application/x-msi" = [ "gjallar-wine-setup.desktop" ];
    };
  };

  # Apps set up before their menu entries came get one, and entries of apps
  # that are gone go. Wine's own menu builder is off in the sandbox, so these
  # are the only entries Wine apps have.
  home.activation.gjallarWineMenu = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    run ${sandboxLauncher}/libexec/gjallar-wine menu
  '';
}
