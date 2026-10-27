{
  nixpkgs,
  system,
  thunderbirdModules,
}:
let
  evaluate =
    extraSettings:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = thunderbirdModules ++ [
        {
          system.stateVersion = "26.05";
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "btrfs";
          };
          boot.loader.grub.enable = false;
        }
      ];
      specialArgs.settings = {
        apps = [ "thunderbird" ];
      }
      // extraSettings;
    }).config;
  plain = evaluate { };
  card = evaluate { thunderbirdSmartCard = true; };
  absent = evaluate { apps = [ ]; };
  policies = cfg: cfg.environment.etc."thunderbird/policies/policies.json".source;
  scdaemon = card.environment.etc."gnupg/scdaemon.conf".text;
  opensc = card.environment.etc."gjallar/pkcs11/opensc-pkcs11.so".source;
in
assert !(absent.environment.etc ? "thunderbird/policies/policies.json");
assert !plain.services.pcscd.enable;
assert !(plain.environment.etc ? "gnupg/scdaemon.conf");
assert !(plain.environment.etc ? "gjallar/pkcs11/opensc-pkcs11.so");
assert card.services.pcscd.enable;
assert card.programs.gnupg.agent.enable;
# scdaemon has to share the reader with OpenSC.
assert nixpkgs.lib.hasInfix "disable-ccid" scdaemon;
assert nixpkgs.lib.hasInfix "pcsc-shared" scdaemon;
nixpkgs.legacyPackages.${system}.runCommand "gjallar-thunderbird-check"
  { nativeBuildInputs = [ nixpkgs.legacyPackages.${system}.jq ]; }
  ''
    p=${policies plain}
    c=${policies card}
    jq -e '.policies.PrimaryPassword == true' "$p"
    jq -e '.policies.DisableAppUpdate == true and .policies.DisableTelemetry == true' "$p"
    jq -e '.policies.SSLVersionMin == "tls1.2"' "$p"
    jq -e '.policies.InAppNotification | .Disabled and (.DonationEnabled or .SurveyEnabled or .MessageEnabled | not)' "$p"
    jq -e '.policies.ExtensionSettings["*"].allowed_types == ["dictionary", "locale", "theme"]' "$p"
    pref() { jq -e --arg k "$2" --argjson v "$3" --arg s "$4" '.policies.Preferences[$k] == {Value: $v, Status: $s}' "$1"; }
    pref "$p" mailnews.message_display.disable_remote_image true locked
    pref "$p" mailnews.auto_config.fetchFromISP.sslOnly true locked
    pref "$p" mailnews.auto_config.guess.sslOnly true locked
    pref "$p" mail.phishing.detection.enabled true locked
    pref "$p" mail.chat.enabled false locked
    pref "$p" mailnews.start_page.override_url '""' locked
    pref "$p" mail.openpgp.allow_external_gnupg false locked
    pref "$c" mail.openpgp.allow_external_gnupg true locked
    pref "$p" mailnews.display.html_as 3 default
    pref "$p" mailnews.headers.sendUserAgent false default
    # The start page is the local guide and loads nothing remote.
    start=$(jq -r '.policies.Preferences["mailnews.start_page.url"].Value' "$p")
    case "$start" in file:///nix/store/*) ;; *) exit 1 ;; esac
    test -s "''${start#file://}"
    if grep -Eq '(src|href)="https?:' "''${start#file://}"; then exit 1; fi
    jq -e '.policies | has("SecurityDevices") | not' "$p"
    jq -e '.policies.SecurityDevices.Add["OpenSC smart card"] == "/etc/gjallar/pkcs11/opensc-pkcs11.so"' "$c"
    jq -e '.policies.SecurityDevices.Delete == ["OpenSC"]' "$c"
    test -e ${opensc}
    touch "$out"
  ''
