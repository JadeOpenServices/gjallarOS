# USB is closed from the first moment of boot, and stage 1 lets in only the
# built-in keyboard the owner needs for the disk unlock (Plane 194, tier
# "usb-keyboard"). Every other device waits for USBGuard and the USB trust
# broker in stage 2, the same as a device plugged in later.
#
# usbcore.authorized_default=0 makes every new device start deauthorized;
# root hubs are always let in (drivers/usb/core/usb.c, usb_dev_authorized).
# USBGuard writes the same default when it starts (AuthorizedDefault=none),
# so the kernel parameter only moves that state to the start of boot. The
# allow rule lives in the initrd only: a keyboard plugged in again in stage
# 2 before USBGuard runs stays closed until USBGuard decides.
#
# Of the keyboard only the input parts (HID interfaces) are let in, so a
# keyboard that also offers storage or a network adapter gives stage 1 the
# keys and nothing else.
#
# The keyboard is let in by its root port, its root hub's USB version and
# its controller's PCI ID, read the way the broker reads them
# (internal/usbtrust/roothub), and its vendor:product. Anything that does
# not match, or a controller ID that is not unique, stays closed.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.gjallar-usbtrust.earlyKeyboardGate;

  # $1 kernel name (1-4), $2 vendor:product, $3 controller vendor:device,
  # $4 usb2 or usb3, $5 root port. Logs why a device stays closed, so a
  # missed keyboard shows in dmesg.
  allowKeyboard = pkgs.writeShellScript "gjallar-usb-keyboard-gate" ''
    log() { echo "<6>gjallar-usb-keyboard-gate: $1: $2" > /dev/kmsg; }
    if ! [[ $1 =~ ^([0-9]+)-([0-9]+)$ ]]; then
      log "$1" "not on a root port, stays closed"
      exit 0
    fi
    bus=''${BASH_REMATCH[1]}
    if [[ ''${BASH_REMATCH[2]} != "$5" ]]; then
      log "$1" "not on root port $5, stays closed"
      exit 0
    fi
    dev=/sys/bus/usb/devices/$1
    read -r vendor < "$dev/idVendor" && read -r product < "$dev/idProduct" || exit 1
    [[ $vendor:$product == "$2" ]] || exit 0

    # PCI devices are all known before init runs, so this count does not
    # race the other controllers' probe.
    controllers=()
    for pci in /sys/bus/pci/devices/*; do
      read -r v < "$pci/vendor" && read -r d < "$pci/device" || continue
      if [[ ''${v#0x}:''${d#0x} == "$3" ]]; then
        controllers+=("$(${pkgs.coreutils}/bin/readlink -f "$pci")")
      fi
    done
    if (( ''${#controllers[@]} != 1 )); then
      log "$1" "''${#controllers[@]} controllers with ID $3, stays closed"
      exit 0
    fi
    hub=$(${pkgs.coreutils}/bin/readlink -f "/sys/bus/usb/devices/usb$bus") || exit 1
    if [[ ''${hub%/*} != "''${controllers[0]}" ]]; then
      log "$1" "not on controller $3, stays closed"
      exit 0
    fi
    read -r version < "$hub/version" || exit 1
    if [[ usb''${version%%.*} != "$4" ]]; then
      log "$1" "not on the $4 root hub, stays closed"
      exit 0
    fi

    # Only the keyboard's input parts (interface class 03, HID) get a
    # driver; anything else the same device offers stays closed (Q88). New
    # interfaces take the root hub's default, so it is 0 while the keyboard
    # is set up and 1 again right after, the way USBGuard finds it in
    # stage 2.
    default=$hub/interface_authorized_default
    echo 0 > "$default" || exit 1
    trap 'echo 1 > "$default"' EXIT
    echo 1 > "$dev/authorized"
    echo 1 > "$default"
    trap - EXIT
    input=()
    for intf in "$dev/$1":*; do
      read -r class < "$intf/bInterfaceClass" || continue
      if [[ $class == 03 ]]; then
        input+=("''${intf##*/}")
      else
        log "''${intf##*/}" "class $class is not an input part, stays closed"
      fi
    done
    if (( ''${#input[@]} == 0 )); then
      echo 0 > "$dev/authorized"
      log "$1" "no input part, stays closed"
      exit 0
    fi
    # All parts first, then the probe, as the kernel's USB authorization
    # docs ask for drivers that use more than one interface.
    for name in "''${input[@]}"; do
      echo 1 > "$dev/$name/authorized"
    done
    for name in "''${input[@]}"; do
      echo "$name" > /sys/bus/usb/drivers_probe
    done
    log "$1" "built-in keyboard let in, input parts ''${input[*]}"
  '';

  # The one rule, from the devices file. Facts that do not look like IDs
  # and ports fail the build instead of ending up in a udev rule.
  rules =
    pkgs.runCommand "gjallar-usb-keyboard-gate-rules"
      {
        nativeBuildInputs = [ pkgs.jq ];
        inherit (cfg) devices;
        helper = allowKeyboard;
      }
      ''
        mkdir -p $out/lib/udev/rules.d
        jq -r --arg helper "$helper" '
          [.devices[] | select(.path | startswith("hardware.input.keyboard.")) | select(.usbPort)]
          | if length == 1 then .[0]
            else error("USB keyboard gate: expected one USB keyboard, found \(length)") end
          | if (.deviceId | test("^[0-9a-f]{4}:[0-9a-f]{4}$"))
                and (.usbPort.controller | test("^[0-9a-f]{4}:[0-9a-f]{4}$"))
                and (.usbPort.hub == "usb2" or .usbPort.hub == "usb3")
                and (.usbPort.port | type == "number" and . >= 1 and . <= 255 and floor == .)
            then . else error("USB keyboard gate: keyboard facts are not an ID and a root port") end
          | (.deviceId | split(":")) as [$vid, $pid]
          | "ACTION==\"add\", SUBSYSTEM==\"usb\", ENV{DEVTYPE}==\"usb_device\", ATTR{idVendor}==\"\($vid)\", ATTR{idProduct}==\"\($pid)\", RUN+=\"\($helper) %k \(.deviceId) \(.usbPort.controller) \(.usbPort.hub) \(.usbPort.port)\""
        ' "$devices" > $out/lib/udev/rules.d/90-gjallar-usb-keyboard-gate.rules
      '';
in
{
  options.services.gjallar-usbtrust.earlyKeyboardGate = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      internal = true;
      description = "Close USB from the start of boot and let only the built-in USB keyboard in during stage 1. Set by the USB trust module for the usb-keyboard tier.";
    };
    devices = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      internal = true;
      description = "`oddc devices --json` output that names the one built-in USB keyboard and its root port.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = config.boot.initrd.systemd.enable;
        message = "The early USB keyboard gate needs systemd in stage 1.";
      }
      {
        assertion = cfg.devices != null;
        message = "The early USB keyboard gate needs the ODDC devices file.";
      }
    ];
    boot.kernelParams = [ "usbcore.authorized_default=0" ];
    boot.initrd.services.udev.packages = [ rules ];
    boot.initrd.systemd.storePaths = [ allowKeyboard ];
  };
}
