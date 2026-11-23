{
  config,
  lib,
  pkgs,
  utils,
  oddcSource,
  ...
}:
# The installer stick (Plane 344/350). The same image for everyone: built
# from the source without generated/ or user.config.json, US keyboard until
# the stick menu asks. tty1 runs the stick menu: in a window when the
# screen can show one, on the text console otherwise.
let
  media = config.system.build.gjallarMedia;
  isoName = "gjallar-stick-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}.iso";

  # No gjallar.iso=: stage 1 takes the volume labelled GJALLAR_STICK, and
  # gjallar.verity= decides whether its store may run.
  uki = media.mkUki {
    name = "gjallar-stick.efi";
    prettyName = "gjallarOS installer";
    cmdline = "";
  };

  installer = pkgs.callPackage ../../pkgs/gjallar-installer { oddc = oddcSource; };

  # Before it installs, the menu checks the stick against the newest signed
  # release (Q233): below the release floor it refuses. The stick's release
  # is system/release, the tree it installs. A file in the store, so the
  # root menu reads it from the verity-checked image.
  releaseConfig = pkgs.writeText "gjallar-stick-release.json" (
    builtins.toJSON {
      version = lib.fileContents ../release;
      inherit (config.gjallar.stick.release) url key;
    }
  );

  # The window draws with the CPU (WLR_RENDERER=pixman): mesa's GPU drivers
  # would add about 840 MB to an image that has about 150 MB left. Nothing
  # in the window is an X program, so cage comes without Xwayland.
  cage = pkgs.cage.override {
    wlroots_0_20 = pkgs.wlroots_0_20.override { enableXWayland = false; };
  };

  # Jade Code (Fira Code with the Fira names changed, OFL 1.1), the
  # brand's terminal font. Its licence goes with it.
  jadeCode = pkgs.runCommand "jade-code" { } ''
    install -Dm444 ${../../themes/brand/openjade/fonts/JadeCode-VF.ttf} $out/share/fonts/truetype/JadeCode-VF.ttf
    install -Dm444 ${../../themes/brand/openjade/fonts/JadeCode-LICENSE.txt} $out/share/fonts/truetype/JadeCode-LICENSE.txt
  '';
  # The live system turns fontconfig off; foot gets its own, with one font.
  footFonts = pkgs.makeFontsConf { fontDirectories = [ jadeCode ]; };
  # The desktop's font size.
  inherit (import ../../themes/noctalia.nix { inherit pkgs; }) fontSize;

  # The text console's font for the menu when no window could start: larger
  # on screens wide enough to keep at least 100 columns. Store files only.
  consoleFonts = "${pkgs.terminus_font}/share/consolefonts";

  # The brand's terminal colours (Obsidian, dark), as the installed
  # desktop's terminal has them. foot and the console take them without '#'.
  term = (import ../../themes/palettes/openjade.nix).Obsidian.dark.terminal;
  hex = lib.removePrefix "#";
  slots = [
    "black"
    "red"
    "green"
    "yellow"
    "blue"
    "magenta"
    "cyan"
    "white"
  ];
  normal = map (n: hex term.normal.${n}) slots;
  bright = map (n: hex term.bright.${n}) slots;
  footColours = lib.concatStrings (
    lib.imap0 (i: c: "regular${toString i}=${c}\n") normal
    ++ lib.imap0 (i: c: "bright${toString i}=${c}\n") bright
  );
  # The Linux console paints the screen with colour 0 and writes text in
  # colour 7, so those two are the window's background and text.
  consoleColours = [
    (hex term.background)
  ]
  ++ lib.sublist 1 6 normal
  ++ [ (hex term.foreground) ]
  ++ bright;

  # The terminal runs as root, like the menu in it. Its own shortcuts that
  # start programs (a new terminal, opening a link) are off: the menu's
  # Shell entry is the one way to a shell. The window keeps the screen's
  # full size, not whole character cells.
  footConfig = pkgs.writeText "gjallar-stick-foot.ini" ''
    [main]
    font=Jade Code:style=Regular:size=${toString fontSize}
    dpi-aware=no
    pad=0x0 center
    resize-by-cells=no
    term=xterm-256color
    initial-color-theme=dark

    [colors-dark]
    alpha=1.0
    foreground=${hex term.foreground}
    background=${hex term.background}
    ${footColours}
    [key-bindings]
    spawn-terminal=none
    show-urls-launch=none
    show-urls-copy=none
    show-urls-persistent=none

    [mouse]
    hide-when-typing=yes
  '';

  # The menu's Shell entry (shellCommand in internal/installer/stick): the
  # nixos user's login shell in the page frame (calmmenu.FrameFor), so it
  # keeps the same margin as the menu pages. The Linux console has no left
  # and right margins of its own; a tmux popup on the frame gives them.
  # tmux is only the frame: no status line, no keys or mouse of its own,
  # and it ends when the shell ends. Takes the frame's first column and
  # row (from 1), then its width and height in cells.
  shellTmuxConf = pkgs.writeText "gjallar-stick-shell.tmux.conf" ''
    set -g status off
    set -g mouse off
    set -g prefix None
    set -g prefix2 None
    unbind -a -T prefix
    unbind -a -T root
    set -sg escape-time 0
  '';
  # display-popup returns the status of the command in it. The shell's own
  # status (the last command before exit) is not a frame failure, so the
  # frame ends with 0 and only tmux itself can report one.
  shellInFrame = pkgs.writeShellScript "gjallar-stick-shell-frame" ''
    printf '%s\n\n' 'Type exit to come back to the stick menu.'
    "$SHELL" -l
    exit 0
  '';
  # The blank window behind the frame. It opens the popup and waits for it,
  # so the tmux session ends with the shell. tmux counts columns from 0 and
  # takes -y as the row under the popup.
  shellWindow = pkgs.writeShellScript "gjallar-stick-shell-window" ''
    if ! ${pkgs.tmux}/bin/tmux display-popup -B -E \
      -x "$(($1 - 1))" -y "$(($2 - 1 + $4))" -w "$3" -h "$4" ${shellInFrame}; then
      printf '%s' 'The shell frame did not open. Press Enter to go back to the stick menu.'
      read -r _
    fi
  '';
  stickShell = pkgs.writeShellApplication {
    name = "gjallar-stick-shell";
    text = ''
      usage() {
        echo "usage: gjallar-stick-shell COLUMN ROW WIDTH HEIGHT" >&2
        exit 2
      }
      [ "$#" -eq 4 ] || usage
      for n in "$@"; do
        case "$n" in
          "" | *[!0-9]*) usage ;;
        esac
      done
      exec ${pkgs.tmux}/bin/tmux -L stick-shell -f ${shellTmuxConf} \
        new-session "${shellWindow} $1 $2 $3 $4"
    '';
  };

  # The menu logo (THEMEBRAIN section 18): the OpenJade lockup (stone and
  # "openJade" as one drawing, never sized apart) on the terminal ground,
  # as sixel at 1x and at 2x (for cells of 48 pixels and more, 4K). The
  # SVG is paths only, so no font is needed. The menu checks both files
  # before it draws them.
  stickLogo =
    pkgs.runCommand "gjallar-stick-logo"
      {
        nativeBuildInputs = [
          pkgs.librsvg
          pkgs.imagemagick
          pkgs.libsixel
        ];
      }
      ''
        mkdir -p $out
        # Render big once, so the trim and the scale down stay sharp.
        rsvg-convert -h 2048 ${../../themes/brand/openjade/lockup-leafgreen-tight.svg} | magick - -trim +repage big.png
        for s in 1 2; do
          # 126 and 252 pixels: four rows of 32 or 64 pixel cells, and a
          # multiple of 6. Sixel draws bands of 6 rows, and a short last
          # band shows as a dark bar under the logo.
          h=$((128 * s / 6 * 6))
          magick big.png -resize "x$h" \
            -background '#${hex term.background}' -alpha remove -alpha off -depth 8 +repage logo.png
          # No dithering and full colour, so the ground keeps the window's
          # background (within sixel's percent steps) and shows no box.
          img2sixel -d none -I logo.png > $out/logo-''${s}x.six
        done
      '';

  # Runs the menu on tty1. Exit 75 means a new keyboard layout (the window
  # starts again with it), 0 a normal end. Any other code from the window
  # (cage or foot could not start, or crashed) moves the menu to the text
  # console for the rest of this boot.
  host = pkgs.writeShellApplication {
    name = "gjallar-stick-host";
    text = ''
      # Only programs from the image: nothing a user on the stick can change
      # is searched (loophole 80).
      export PATH=${
        lib.makeBinPath [
          installer
          cage
          pkgs.foot
          pkgs.ckbcomp
          pkgs.kbd
          pkgs.coreutils
        ]
      }:/run/wrappers/bin:/run/current-system/sw/bin
      # screen sets width and height from the framebuffer; false when it
      # reports nothing usable.
      screen() {
        local size
        size=$(cat /sys/class/graphics/fb0/virtual_size 2> /dev/null) || return 1
        [[ $size =~ ^([0-9]{1,5}),([0-9]{1,5})$ ]] || return 1
        width=''${BASH_REMATCH[1]}
        height=''${BASH_REMATCH[2]}
      }
      console_font() {
        local width height font=
        local -a double=()
        screen || return 0
        if [ "$width" -ge 3840 ] && [ "$height" -ge 2160 ]; then
          font=ter-v32n
          double=(-d)
        elif [ "$width" -ge 1920 ] && [ "$height" -ge 1080 ]; then
          font=ter-v32n
        elif [ "$width" -ge 1280 ] && [ "$height" -ge 720 ]; then
          font=ter-v24n
        fi
        [ -z "$font" ] || setfont "''${double[@]}" ${consoleFonts}/$font.psf.gz || true
      }
      window=true
      while true; do
        if [ "$window" = true ]; then
          layout=$(gjallar-stick layout)
          # The window font scales with the screen height in pixels: 19 at
          # 1080 gives about 120 by 33 cells, like Terminus on the console.
          # dpi-aware=no above, so a wrong EDID cannot change it.
          font=()
          if screen && [ "$height" -ge 600 ]; then
            font=(-o "main.font=Jade Code:style=Regular:size=$((19 * height / 1080))")
          fi
          rc=0
          # -d tells foot to draw no title bar (cage draws none either).
          # Without it foot kept 26 pixels for one and left a black strip at
          # the bottom of the screen.
          XKB_DEFAULT_LAYOUT=$layout WLR_RENDERER=pixman FONTCONFIG_FILE=${footFonts} \
            cage -d -- foot -c ${footConfig} "''${font[@]}" -- gjallar-stick menu --mode window --release ${releaseConfig} --logo ${stickLogo} || rc=$?
          case $rc in
            0 | 75) continue ;;
          esac
          window=false
          # logind gives tty1 back from cage (text mode, keyboard on) a
          # moment late, and the kernel takes no font in graphics mode: the
          # host owns tty1 and does it itself.
          gjallar-stick text-console || true
          printf '%s\n' "The window stopped (exit code $rc). The menu goes on in the text console."
          # A quiet boot defers fbcon: tty1 stays on the dummy console,
          # which takes no font, until the first text reaches it (setfont
          # failed there, D4 2026-11-16). The font comes after that line;
          # fbcon redraws it.
          console_font
        fi
        # The console has no window to catch the installer's errors: they
        # go to the screen too.
        rc=0
        gjallar-stick menu --mode console --release ${releaseConfig} 2>&1 || rc=$?
        case $rc in
          0 | 75) ;;
          *) sleep 1 ;;
        esac
      done
    '';
  };
in
{
  imports = [
    ./live.nix
    {
      options.gjallar.stick.release = {
        url = lib.mkOption {
          type = lib.types.strMatching "https://[^?#]+[^/?#]";
          default = "https://github.com/JadeOpenServices/gjallarOS/releases/latest/download";
          description = "Folder with the newest release's SHA256SUMS, SHA256SUMS.sig and RELEASE.";
        };
        key = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            The release key (PEM). null builds a stick that refuses to
            install: it cannot check its release. CP-H sets the real key.
          '';
        };
      };
    }
  ];

  # No boot screen for now: a Restart from the text menu never showed its
  # restart screen (Plane 360 brings it back). The boot stays quiet; the
  # menu is the first thing on screen.
  boot.consoleLogLevel = 0;
  boot.initrd.verbose = false;
  boot.kernelParams = [
    "quiet"
    "rd.systemd.show_status=false"
    "systemd.show_status=false"
    "udev.log_level=0"
    "rd.udev.log_level=0"
  ];

  # The text console in the same colours, for the menu when no window
  # could start.
  console.colors = consoleColours;

  # targetdisk.bootMediaLabel knows this label; the installer never offers
  # the stick as a target.
  isoImage.volumeID = "GJALLAR_STICK";
  image.baseName = lib.mkForce (lib.removeSuffix ".iso" isoName);

  networking.hostName = "gjallar-stick";

  # The installer queues the removal of the stick's hash from shim's list
  # (QueueStickHashDelete); mokutil does that.
  environment.systemPackages = [
    stickShell
  ]
  ++ lib.optional pkgs.stdenv.hostPlatform.isx86_64 pkgs.mokutil;

  # tty1 is the stick menu, not a login. The other consoles keep the live
  # login of every NixOS installer; the menu's Shell entry gives the same.
  systemd.services."getty@tty1".enable = false;
  systemd.services."autovt@tty1".enable = false;
  systemd.tmpfiles.rules = [
    "d /run/gjallar 0755 root root - -"
    "d /run/gjallar/stick 0755 root root - -"
    "d /run/gjallar/stick/logs 0700 root root - -"
  ];
  systemd.services.gjallar-stick = {
    description = "gjallarOS installer stick menu";
    wantedBy = [ "multi-user.target" ];
    after = [
      "installer-repository.service"
      "systemd-user-sessions.service"
      "systemd-logind.service"
    ];
    wants = [ "systemd-logind.service" ];
    unitConfig.ConditionPathExists = "/dev/tty1";
    restartIfChanged = false;
    environment = {
      TERM = "linux";
      LANG = "C.UTF-8";
    };
    serviceConfig = {
      ExecStart = lib.getExe host;
      # A root session on seat0 through logind lets cage open the screen
      # and the keyboard.
      User = "root";
      PAMName = "gjallar-stick";
      TTYPath = "/dev/tty1";
      TTYReset = true;
      TTYVHangup = true;
      TTYVTDisallocate = true;
      StandardInput = "tty-fail";
      StandardOutput = "tty";
      # cage and foot log here; the menu's own log is in its log folder.
      StandardError = "journal";
      Restart = "always";
      RestartSec = 1;
    };
  };
  # Only the session part runs for a service. Nobody signs in through it.
  security.pam.services.gjallar-stick = {
    useDefaultRules = false;
    rules = {
      auth = utils.pam.autoOrderRules [
        {
          name = "deny";
          control = "required";
          modulePath = "${config.security.pam.package}/lib/security/pam_deny.so";
        }
      ];
      account = utils.pam.autoOrderRules [
        {
          name = "unix";
          control = "required";
          modulePath = "${config.security.pam.package}/lib/security/pam_unix.so";
        }
      ];
      session = utils.pam.autoOrderRules [
        {
          name = "unix";
          control = "required";
          modulePath = "${config.security.pam.package}/lib/security/pam_unix.so";
        }
        {
          name = "systemd";
          control = "required";
          modulePath = "${config.systemd.package}/lib/security/pam_systemd.so";
        }
      ];
    };
  };

  # Started through shim, the stick's UKI is trusted by its hash, which the
  # user enrolled in MokManager. The installer removes that hash again after
  # the install (CP-C, Q1). It needs the UKI to compute the hash, and with
  # copytoram the stick may be gone by then, so stage 1 keeps a copy in
  # /run/gjallar, which carries into stage 2 and only root can change.
  # Only the disk stage 1 booted from is read, and only its one EFI system
  # partition. The stick is MBR, so systemd-stub reports no partition
  # (StubDevicePartUUID); StubImageIdentifier says which file it started.
  # A failure here never stops the boot: the installer then says it could
  # not find the hash.
  boot.initrd.systemd.services.gjallar-stick-uki = lib.mkIf pkgs.stdenv.hostPlatform.isx86_64 {
    description = "Keep the stick's boot file for the installer";
    wantedBy = [ "initrd.target" ];
    # It reads the device behind gjallar-iso, so it has nothing to do when
    # that failed; the retry at the emergency prompt starts both again.
    requires = [ "gjallar-media-iso.service" ];
    after = [ "gjallar-media-iso.service" ];
    before = [ "gjallar-media-store.service" ];
    unitConfig.DefaultDependencies = false;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
    path = [
      pkgs.coreutils
      config.boot.initrd.systemd.package.util-linux
      config.services.lvm.package
    ];
    script = ''
      set -euo pipefail
      fail() {
        echo "$*" >&2
        exit 1
      }
      efivars=/sys/firmware/efi/efivars
      if ! findmnt -n "$efivars" > /dev/null; then
        mount -t efivarfs -o ro,nodev,nosuid,noexec efivarfs "$efivars"
      fi
      var=$efivars/StubImageIdentifier-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
      [ -f "$var" ] || exit 0
      # efivars start with 4 attribute bytes; the value is UTF-16.
      id=$(tail -c +5 "$var" | tr -d '\000' | tr '[:upper:]' '[:lower:]')
      if [ "$id" != '\efi\boot\grubx64.efi' ]; then
        # Started directly, not through shim: no hash to remove.
        exit 0
      fi
      # The device stage 1 set up gjallar-iso from, not a fresh label lookup
      # that another stick with the same label could answer.
      deps=$(dmsetup deps -o devname gjallar-iso)
      back=''${deps##*(}
      back=''${back%)}
      [[ $back =~ ^[a-z0-9]+$ ]] || fail "The boot medium has no plain device name."
      dev=$(readlink -f "/sys/class/block/$back")
      if [ -f "$dev/partition" ]; then
        dev=$(dirname "$dev")
      fi
      esp=
      for part in "$dev"/*/partition; do
        [ -f "$part" ] || continue
        node=/dev/$(basename "$(dirname "$part")")
        if [ "$(blkid -p -o value -s PART_ENTRY_TYPE "$node")" = 0xef ] \
          && [ "$(blkid -p -o value -s TYPE "$node")" = vfat ]; then
          [ -z "$esp" ] || fail "The stick has more than one EFI system partition."
          esp=$node
        fi
      done
      [ -n "$esp" ] || fail "The stick has no EFI system partition."
      # gjallar-iso maps the whole disk and so holds it exclusively; the
      # kernel then refuses the exclusive open a mount of one of its
      # partitions needs. A read-only loop device opens the partition
      # without that claim.
      loop=
      mounted=
      cleanup() {
        [ -z "$mounted" ] || umount /run/gjallar/stick-esp || true
        [ -z "$loop" ] || losetup -d "$loop" || true
        rmdir /run/gjallar/stick-esp 2> /dev/null || true
        rm -f /run/gjallar/stick-uki.efi.new
      }
      trap cleanup EXIT
      mkdir -p -m 0755 /run/gjallar /run/gjallar/stick-esp
      loop=$(losetup -f --show -r "$esp")
      mount -t vfat -o ro,nodev,nosuid,noexec "$loop" /run/gjallar/stick-esp
      mounted=1
      file=/run/gjallar/stick-esp/EFI/BOOT/grubx64.efi
      [ -f "$file" ] || fail "The stick's boot file is missing."
      # The installer reads at most this much (stickUKIMax).
      cap=$((128 * 1024 * 1024))
      head -c $((cap + 1)) "$file" > /run/gjallar/stick-uki.efi.new
      [ "$(stat -c %s /run/gjallar/stick-uki.efi.new)" -le "$cap" ] || fail "The stick's boot file is too large."
      chmod 0444 /run/gjallar/stick-uki.efi.new
      mv -f /run/gjallar/stick-uki.efi.new /run/gjallar/stick-uki.efi
    '';
  };

  system.build.gjallarStickIso = pkgs.callPackage ../../pkgs/gjallar-stick-iso {
    inherit uki isoName;
    inherit (media) store storeVerity;
    inherit (config.isoImage) volumeID;
    inherit (pkgs.stdenv.hostPlatform) efiArch;
    # Debian's shim exists for x86_64 only.
    shim =
      if pkgs.stdenv.hostPlatform.isx86_64 then pkgs.callPackage ../../pkgs/gjallar-shim { } else null;
  };
}
