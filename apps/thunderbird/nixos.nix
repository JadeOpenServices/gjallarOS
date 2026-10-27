{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

# Thunderbird, latest release, with privacy and security defaults set by
# enterprise policy (ROADMAP section 20). The user sets up every account;
# a local start page walks through it.
let
  smartCard = settings.thunderbirdSmartCard or false;

  guide = pkgs.writeTextDir "share/gjallar-thunderbird/start.html" (builtins.readFile ./start.html);

  locked = Value: {
    inherit Value;
    Status = "locked";
  };
  # The user can change these in Thunderbird.
  preset = Value: {
    inherit Value;
    Status = "default";
  };
in
lib.mkIf config.gjallar.apps.thunderbird.enable (
  lib.mkMerge [
    {
      programs.thunderbird = {
        enable = true;
        package = pkgs.thunderbird-latest;
        policies = {
          # Account setup, OAuth and saved passwords ask for one first, and
          # it cannot be removed.
          PrimaryPassword = true;
          DisableTelemetry = true;
          InAppNotification = {
            Disabled = true;
            DonationEnabled = false;
            SurveyEnabled = false;
            MessageEnabled = false;
          };
          SSLVersionMin = "tls1.2";
          # No add-ons yet (no catalogue pins); dictionaries, languages and
          # themes carry no code.
          ExtensionSettings."*" = {
            installation_mode = "allowed";
            allowed_types = [
              "dictionary"
              "locale"
              "theme"
            ];
            blocked_install_message = "GjallarOS allows only dictionaries, languages and themes in Thunderbird.";
          };
          Preferences = {
            # Account autoconfig over HTTPS only.
            "mailnews.auto_config.fetchFromISP.sslOnly" = locked true;
            "mailnews.auto_config.guess.sslOnly" = locked true;
            "mailnews.message_display.disable_remote_image" = locked true;
            "mail.phishing.detection.enabled" = locked true;
            "datareporting.policy.dataSubmissionEnabled" = locked false;
            "mail.chat.enabled" = locked false;
            # No remote "what's new" page after an update.
            "mailnews.start_page.override_url" = locked "";
            "mailnews.start_page.url" = locked "file://${guide}/share/gjallar-thunderbird/start.html";
            "mail.openpgp.allow_external_gnupg" = locked smartCard;
            # Simple HTML: layout stays, scripts, forms and remote parts go.
            "mailnews.display.html_as" = preset 3;
            "mailnews.display.disallow_mime_handlers" = preset 3;
            "mailnews.headers.sendUserAgent" = preset false;
            # The desktop picks the mail app (apps/thunderbird/home.nix).
            "mail.shell.checkDefaultClient" = preset false;
          };
        };
      };
    }

    # Opt-in smart card: OpenPGP cards go through gpg-agent and scdaemon,
    # S/MIME cards through OpenSC. Both talk to the reader through pcscd,
    # which starts only when something asks for it.
    (lib.mkIf smartCard {
      services.pcscd.enable = true;
      programs.gnupg.agent = {
        enable = true;
        pinentryPackage = pkgs.pinentry-qt;
      };
      # scdaemon would otherwise claim the reader itself and lock out OpenSC.
      environment.etc."gnupg/scdaemon.conf".text = ''
        disable-ccid
        pcsc-shared
      '';
      # Thunderbird keeps the module path in each profile, so it gets a path
      # that survives OpenSC updates and garbage collection. "OpenSC" was the
      # old entry with a store path; Delete runs before Add.
      environment.etc."gjallar/pkcs11/opensc-pkcs11.so".source = "${pkgs.opensc}/lib/opensc-pkcs11.so";
      programs.thunderbird.policies.SecurityDevices = {
        Delete = [ "OpenSC" ];
        Add."OpenSC smart card" = "/etc/gjallar/pkcs11/opensc-pkcs11.so";
      };
    })
  ]
)
