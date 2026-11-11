{ lib, pkgs, ... }:
let
  libexec = import ../../pkgs/lib/libexec.nix { inherit lib pkgs; };

  # Opens a security key registration link in a plain Chromium window with a
  # throwaway profile. Chromium asks to set a PIN on keys that have none;
  # LibreWolf does not. Key access comes from systemd's 60-fido-id.rules.
  # Started by gjallarctl register-key (Plane 282); not on PATH.
  registerKey = pkgs.writeShellApplication {
    name = "gjallar-register-key";
    runtimeInputs = [ pkgs.coreutils pkgs.zenity ];
    text = ''
      url="''${1:-}"
      if [ -z "$url" ]; then
        url=$(zenity --entry --title="Register key" \
          --text="Link from the registration page:") || exit 0
      fi
      case "$url" in
        https://*) ;;
        *)
          zenity --error --title="Register key" --text="Only https links can be opened."
          exit 2
          ;;
      esac

      profile=$(mktemp -d "''${XDG_RUNTIME_DIR:-/tmp}/register-key.XXXXXX")
      trap 'rm -rf "$profile"' EXIT
      ${lib.getExe pkgs.ungoogled-chromium} \
        --no-first-run \
        --no-default-browser-check \
        --disable-sync \
        --disable-background-mode \
        --user-data-dir="$profile" \
        --app="$url"
    '';
  };
in
{
  # Policies for every ungoogled-chromium window: the Plane web application
  # and the key registration launcher. Only writes
  # /etc/chromium/policies; the browser comes with the apps that use it.
  programs.chromium = {
    enable = true;
    extraOpts = {
      MetricsReportingEnabled = false;
      UrlKeyedAnonymizedDataCollectionEnabled = false;
      UserFeedbackAllowed = false;
      WebRtcEventLogCollectionAllowed = false;

      BlockThirdPartyCookies = true;

      SyncDisabled = true;
      BackgroundModeEnabled = false;
      DefaultBrowserSettingEnabled = false;

      AutofillAddressEnabled = false;
      AutofillCreditCardEnabled = false;
      PasswordManagerEnabled = false;
      PaymentMethodQueryEnabled = false;
      PromotionalTabsEnabled = false;
    };
  };

  # No menu entry: a system tool, started only as gjallarctl register-key.
  environment.systemPackages = [ (libexec registerKey) ];
}
