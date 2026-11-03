{
  config,
  lib,
  pkgs,
  ...
}:

# Boot screen, layout C (Jade, 2026-10-27): the mark spins next to the still
# wordmark, and a line in the wordmark's font sits near the bottom. The mark,
# the wordmark and the lines are plain options, so a user, a company or a
# JODS profile can swap any of them.
#
# Plymouth only loads PNG. SVG is the source: the build renders it, trims
# it and re-encodes it, so a supplied file never reaches the boot as is.
let
  cfg = config.gjallar.bootSplash;
  brand = ../brand/openjade;

  imageFile = lib.types.addCheck lib.types.path (
    file: builtins.match ".*\\.(svg|png)" (lib.toLower (toString file)) != null
  )
  // {
    description = "path to an SVG or PNG file";
  };

  # The lines are drawn once at build time, 80 px type on a 128 px strip.
  line = lib.types.addCheck lib.types.str (
    text:
    lib.stringLength text <= 60
    && builtins.match ".*[^[:space:]].*" text != null
    && builtins.match ".*[[:cntrl:]].*" text == null
  )
  // {
    description = "line of 1 to 60 printable characters";
  };

  lineList =
    lib.types.addCheck (lib.types.nonEmptyListOf line) (lines: builtins.length lines <= 16)
    // {
      description = "list of 1 to 16 lines";
    };

  color = lib.types.strMatching "#[0-9a-fA-F]{6}";

  # Plymouth takes colours as 0..1 per channel.
  channels =
    name: hex:
    lib.concatStrings (
      lib.zipListsWith (
        channel: i: ''
          ${name}_${channel} = ${toString (lib.fromHexString (builtins.substring i 2 hex) / 255.0)};
        ''
      ) [ "r" "g" "b" ] [ 1 3 5 ]
    );

  modes = [
    "boot"
    "reboot"
    "shutdown"
  ];

  lineSvg =
    mode: index: text:
    pkgs.writeText "boot-line-${mode}-${toString index}.svg" ''
      <svg xmlns="http://www.w3.org/2000/svg" width="5200" height="128">
        <text x="16" y="96" font-family="${lib.escapeXML cfg.font.family}" font-weight="${toString cfg.font.weight}" font-size="80" fill="${cfg.textColor}">${lib.escapeXML text}</text>
      </svg>
    '';

  lineFiles = lib.concatMap (
    mode:
    lib.imap0 (index: text: {
      name = "line-${mode}-${toString index}.png";
      svg = lineSvg mode index text;
    }) cfg.lines.${mode}
  ) modes;

  # The script reads its sizes and file names from this header.
  header =
    channels "background" cfg.background
    + channels "text" cfg.textColor
    + ''
    prompt_font = "${cfg.font.family} 14";
    spin = ${if cfg.spin then "1" else "0"};
    has_wordmark = ${if cfg.wordmark == null then "0" else "1"};
  ''
  + lib.concatMapStrings (
    mode:
    lib.concatStrings (
      lib.imap0 (index: _: ''
        lines.${mode}[${toString index}] = "line-${mode}-${toString index}.png";
      '') cfg.lines.${mode}
    )
    + ''
      line_count.${mode} = ${toString (builtins.length cfg.lines.${mode})};
    ''
  ) modes;

  theme =
    pkgs.runCommand "gjallar-plymouth-theme"
      {
        nativeBuildInputs = [
          pkgs.librsvg
          pkgs.imagemagick
        ];
        FONTCONFIG_FILE = pkgs.makeFontsConf { fontDirectories = [ cfg.font.package ]; };
        passAsFile = [ "header" ];
        inherit header;
      }
      ''
        export HOME="$TMPDIR"
        dir="$out/share/plymouth/themes/gjallar"
        mkdir -p "$dir"

        im() {
          magick -limit width 16KP -limit height 16KP -limit area 128MP "$@"
        }

        # FILE: sets w and h, and stops the build on an empty picture.
        size() {
          dims=$(magick identify -format '%w %h' "$1")
          case "$dims" in
            [0-9]*" "[0-9]*) ;;
            *)
              echo "boot splash: cannot read the size of $1" >&2
              exit 1
              ;;
          esac
          w=''${dims% *}
          h=''${dims#* }
          if [ "$w" -lt 8 ] || [ "$h" -lt 8 ]; then
            echo "boot splash: $1 is empty after trimming" >&2
            exit 1
          fi
        }

        # SRC OUT: trimmed, 384 px high, re-encoded without metadata.
        render() {
          case "$1" in
            *.svg | *.SVG)
              rsvg-convert --width 6144 --height 3072 --keep-aspect-ratio \
                --background-color none --output raw.png "$1"
              ;;
            *) cp "$1" raw.png ;;
          esac
          im raw.png -trim +repage -resize x384 -strip "PNG32:$2"
          size "$2"
        }

        render ${cfg.mark} mark.png
        mark_w=$w
        mark_h=$h
        # Rotate turns the picture inside its own frame; a square as wide as
        # the diagonal keeps the corners on screen.
        pad=$(awk -v w="$w" -v h="$h" 'BEGIN { print int(sqrt(w * w + h * h)) + 2 }')
        im mark.png -background none -gravity center -extent "''${pad}x''${pad}" \
          -strip "PNG32:$dir/mark.png"

        cp "$headerPath" "$dir/gjallar.script"
        {
          echo "mark_w = $mark_w;"
          echo "mark_h = $mark_h;"
          echo "mark_pad = $pad;"
        } >> "$dir/gjallar.script"

        ${lib.optionalString (cfg.wordmark != null) ''
          render ${cfg.wordmark} "$dir/wordmark.png"
          {
            echo "word_w = $w;"
            echo "word_h = $h;"
          } >> "$dir/gjallar.script"
        ''}

        # Only the sides are trimmed, so every line keeps the same strip
        # height and the same type size on screen.
        ${lib.concatMapStrings (file: ''
          rsvg-convert --background-color none --output raw.png ${file.svg}
          im raw.png -define trim:edges=east,west -trim +repage -strip \
            "PNG32:$dir/${file.name}"
        '') lineFiles}

        cat ${./boot-splash.script} >> "$dir/gjallar.script"

        cat > "$dir/gjallar.plymouth" <<EOF
        [Plymouth Theme]
        Name=GjallarOS
        ModuleName=script

        [script]
        ImageDir=$dir
        ScriptFile=$dir/gjallar.script
        EOF
      '';
in
{
  options.gjallar.bootSplash = {
    mark = lib.mkOption {
      type = imageFile;
      default = brand + "/mark.svg";
      description = "The mark on the left of the boot screen; it spins while the system loads.";
    };

    spin = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Whether the mark spins. Turn it off for a mark that looks wrong turned.";
    };

    wordmark = lib.mkOption {
      type = lib.types.nullOr imageFile;
      default = brand + "/wordmark.svg";
      description = "The still text logo right of the mark; null shows the mark alone.";
    };

    font = {
      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.fira-sans;
        description = "Font package for the lines; the wordmark is set in Fira Sans Bold.";
      };
      family = lib.mkOption {
        type = lib.types.strMatching "[A-Za-z0-9 ._-]+";
        default = "Fira Sans";
      };
      weight = lib.mkOption {
        type = lib.types.ints.between 100 900;
        default = 700;
      };
      promptFile = lib.mkOption {
        type = lib.types.path;
        default = "${pkgs.fira-sans}/share/fonts/opentype/FiraSans-Regular.otf";
        description = "The one font file the initrd gets, for password prompts and messages.";
      };
    };

    lines = {
      boot = lib.mkOption {
        type = lineList;
        default = [
          "gjallarOS is loading…"
          "gjallarOS coming right up!"
          "gjallarOS is waking up…"
          "Getting gjallarOS ready for you…"
        ];
        description = "One of these shows at random while the system starts.";
      };
      reboot = lib.mkOption {
        type = lineList;
        default = [
          "gjallarOS is restarting…"
          "Back in a moment…"
        ];
      };
      shutdown = lib.mkOption {
        type = lineList;
        default = [
          "gjallarOS is shutting down…"
          "See you soon!"
        ];
      };
    };

    textColor = lib.mkOption {
      type = color;
      default = "#f6fbf8";
      description = "Colour of the lines, prompts and messages.";
    };

    background = lib.mkOption {
      type = color;
      default = "#${config.lib.stylix.colors.base00}";
      defaultText = lib.literalExpression ''"#''${config.lib.stylix.colors.base00}"'';
    };
  };

  config = lib.mkIf config.boot.plymouth.enable {
    stylix.targets.plymouth.enable = false;

    boot.plymouth = {
      theme = "gjallar";
      themePackages = [ theme ];
      font = cfg.font.promptFile;
    };
  };
}
