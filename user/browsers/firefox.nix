{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  profileName = "Default";
  firefoxAppId = "{ec8030f7-c20a-464f-9b0e-13a3a9e97384}";

  # Firefox installs every add-on from its enterprise policy, from a file in
  # the Nix store: no download at start, and the version only moves with a
  # rebuild. "on" can be turned off in Firefox, "locked" cannot; "off" never
  # reaches this file. Firefox refuses an older file than the one installed,
  # so rolling back a build keeps the newer add-on.
  addonPolicy = mode: xpi: {
    installation_mode =
      {
        on = "normal_installed";
        locked = "force_installed";
      }
      .${mode} or (throw "Firefox add-on mode ${builtins.toJSON mode} is not on or locked");
    install_url = "file://${xpi}";
    updates_disabled = true;
  };

  # Add-ons from the collections in apps/firefox-addons.json, built by NUR.
  addons = pkgs.nur.repos.rycee.firefox-addons;
  catalogue = lib.listToAttrs (
    lib.concatMap (c: map (a: lib.nameValuePair a.id a) c.addons) (
      lib.importJSON ../../apps/firefox-addons.json
    )
  );
  # A config written before add-ons became a choice gets the catalogue
  # defaults, the same ones gjallarctl writes into user.config.json.
  defaultAddons = lib.mapAttrs (_: a: a.defaultMode) (
    lib.filterAttrs (_: a: (a.defaultMode or "off") != "off") catalogue
  );
  chosenAddons = settings.firefoxAddons or defaultAddons;
  catalogueAddon =
    name: mode:
    let
      entry =
        catalogue.${name}
          or (throw "firefoxAddons: ${builtins.toJSON name} is not in apps/firefox-addons.json");
      pkg = addons.${name};
    in
    if pkg.addonId != entry.addonId then
      throw "firefoxAddons: NUR ${name} has ID ${pkg.addonId}, apps/firefox-addons.json says ${entry.addonId}"
    else
      lib.nameValuePair entry.addonId (
        addonPolicy mode "${pkg}/share/mozilla/extensions/${firefoxAppId}/${entry.addonId}.xpi"
      );

  # Add-ons from addons.mozilla.org, pinned to one reviewed file by
  # gjallarctl addons pin. Firefox cancels the install when the file holds
  # another ID. The installer checks these values; the checks here guard
  # hand edits.
  storeAddonIdPattern = "[{][0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}[}]|[a-zA-Z0-9._-]*@[a-zA-Z0-9._-]+";
  storeAddonUrlPattern = "https://addons[.]mozilla[.]org/firefox/downloads/file/[0-9]{1,12}/[A-Za-z0-9._%+-]{1,200}[.]xpi";
  storeAddon =
    a:
    if builtins.match storeAddonIdPattern a.id == null then
      throw "firefoxStoreAddons: ${builtins.toJSON a.id} is not an add-on ID from addons.mozilla.org"
    else if builtins.match storeAddonUrlPattern a.url == null then
      throw "firefoxStoreAddons: ${a.id}: url must be a file on https://addons.mozilla.org/firefox/downloads/file/"
    else if builtins.match "[0-9a-f]{64}" a.sha256 == null then
      throw "firefoxStoreAddons: ${a.id}: sha256 must be 64 lowercase hex digits"
    else
      lib.nameValuePair a.id (
        addonPolicy a.mode (
          pkgs.fetchurl {
            inherit (a) url sha256;
            name = "amo-addon.xpi";
          }
        )
      );

  # Stylix themes Firefox through the Firefox Color add-on. Stylix would link
  # it into the profile; the policy installs it like every other add-on.
  themeAddon = lib.optional config.stylix.targets.firefox.colorTheme.enable (
    let
      pkg = addons.firefox-color;
    in
    lib.nameValuePair pkg.addonId (
      addonPolicy "on" "${pkg}/share/mozilla/extensions/${firefoxAppId}/${pkg.addonId}.xpi"
    )
  );

  extensionSettings = lib.listToAttrs (
    themeAddon
    ++ lib.mapAttrsToList catalogueAddon chosenAddons
    ++ map storeAddon (settings.firefoxStoreAddons or [ ])
  );

  # Firefox keeps a policy add-on when it leaves the policy. The ledger in
  # gjallarctl remembers what the policy installed and removes what is no
  # longer wanted while Firefox is closed.
  desiredAddons = pkgs.writeText "firefox-addons-desired.json" (
    builtins.toJSON { items = lib.attrNames extensionSettings; }
  );
  gjallarctl = pkgs.callPackage ../../pkgs/gjallarctl { };
  profileDir = "%h/${config.programs.firefox.profilesPath}/${
    config.programs.firefox.profiles.${profileName}.path
  }";

  # Hardening that follows LibreWolf's defaults (codeberg.org/librewolf/settings,
  # librewolf.cfg). defaultPref stays changeable in about:config and Settings;
  # lockPref is reserved for telemetry and promotions.
  #
  # Deliberate differences from LibreWolf:
  # - Local Safe Browsing stays on, as in arkenfox; only the remote download
  #   lookups that send file metadata to Google are off.
  # - Nothing is cleared on shutdown, so history, logins and sessions survive.
  # - WebAuthn is left at Firefox defaults; security keys and passkeys must work.
  hardening = ''
    // Tracking protection and isolation
    defaultPref("browser.contentblocking.category", "strict");
    defaultPref("network.cookie.cookieBehavior.optInPartitioning", true);
    defaultPref("privacy.globalprivacycontrol.enabled", true);
    defaultPref("privacy.userContext.enabled", true);
    defaultPref("privacy.userContext.ui.enabled", true);
    defaultPref("cookiebanners.service.mode", 1);
    defaultPref("cookiebanners.service.mode.privateBrowsing", 1);

    // Fingerprinting
    defaultPref("privacy.resistFingerprinting", true);
    defaultPref("privacy.resistFingerprinting.block_mozAddonManager", true);
    defaultPref("privacy.resistFingerprinting.letterboxing", false);
    defaultPref("dom.webgpu.enabled", false);

    // Cache, forms and history
    defaultPref("browser.cache.disk.enable", false);
    defaultPref("browser.privatebrowsing.forceMediaMemoryCache", true);
    defaultPref("browser.shell.shortcutFavicons", false);
    defaultPref("browser.helperApps.deleteTempFileOnExit", true);
    defaultPref("browser.formfill.enable", false);
    defaultPref("browser.sessionstore.privacy_level", 2);

    // Network: no prefetching or speculative connections; DNS stays with the
    // system resolver so VPN DNS applies.
    defaultPref("network.prefetch-next", false);
    defaultPref("network.http.speculative-parallel-limit", 0);
    defaultPref("network.early-hints.preconnect.max_connections", 0);
    defaultPref("browser.places.speculativeConnect.enabled", false);
    defaultPref("browser.urlbar.speculativeConnect.enabled", false);
    defaultPref("network.dns.disablePrefetch", true);
    defaultPref("network.dns.disablePrefetchFromHTTPS", true);
    defaultPref("network.trr.mode", 5);
    defaultPref("doh-rollout.enabled", false);
    defaultPref("network.http.referer.XOriginTrimmingPolicy", 2);
    defaultPref("network.auth.subresource-http-auth-allow", 1);
    defaultPref("network.gio.supported-protocols", "");
    defaultPref("network.proxy.socks_remote_dns", true);
    defaultPref("media.peerconnection.ice.proxy_only_if_behind_proxy", true);
    defaultPref("security.csp.reporting.enabled", false);
    defaultPref("network.captive-portal-service.enabled", false);
    defaultPref("network.connectivity-service.enabled", false);

    // TLS and certificates
    defaultPref("dom.security.https_only_mode", true);
    defaultPref("dom.security.https_only_mode.upgrade_local", true);
    defaultPref("security.cert_pinning.enforcement_level", 2);
    defaultPref("security.ssl.require_safe_negotiation", true);
    defaultPref("security.ssl.treat_unsafe_negotiation_as_broken", true);
    defaultPref("security.pki.crlite_mode", 2);
    defaultPref("security.remote_settings.crlite_filters.enabled", true);
    defaultPref("security.OCSP.enabled", 0);
    defaultPref("security.tls.enable_0rtt_data", false);
    defaultPref("network.http.http3.enable_0rtt", false);
    defaultPref("security.tls.version.enable-deprecated", false);
    defaultPref("browser.xul.error_pages.expert_bad_cert", true);
    defaultPref("network.IDN_show_punycode", true);
    defaultPref("pdfjs.enableScripting", false);
    defaultPref("dom.webserial.enabled", false);
    defaultPref("permissions.manager.defaultsUrl", "");

    // Safe Browsing: local lists on, remote download lookups off
    defaultPref("browser.safebrowsing.downloads.remote.enabled", false);
    defaultPref("browser.safebrowsing.downloads.remote.url", "");

    // Location and region
    defaultPref("geo.provider.network.url", "https://api.beacondb.net/v1/geolocate");
    defaultPref("geo.provider.use_geoclue", false);
    defaultPref("browser.region.network.url", "");
    defaultPref("browser.region.update.enabled", false);

    // DRM stays off until a site asks and the user allows it
    defaultPref("media.eme.enabled", false);

    // Search and address bar
    defaultPref("browser.search.suggest.enabled", false);
    defaultPref("browser.urlbar.suggest.searches", false);
    defaultPref("browser.urlbar.quicksuggest.enabled", false);
    defaultPref("browser.urlbar.suggest.weather", false);
    defaultPref("browser.urlbar.trending.featureGate", false);
    defaultPref("browser.urlbar.addons.featureGate", false);
    defaultPref("browser.urlbar.mdn.featureGate", false);
    defaultPref("browser.urlbar.yelp.featureGate", false);
    defaultPref("browser.search.separatePrivateDefault", true);
    defaultPref("browser.search.separatePrivateDefault.ui.enabled", true);

    // Behaviour
    defaultPref("browser.download.useDownloadDir", false);
    defaultPref("browser.download.manager.addToRecentDocs", false);
    defaultPref("browser.download.start_downloads_in_tmp_dir", true);
    defaultPref("media.autoplay.default", 5);
    defaultPref("dom.disable_window_move_resize", true);
    defaultPref("browser.link.open_newwindow", 3);
    defaultPref("browser.link.open_newwindow.restriction", 0);
    defaultPref("browser.tabs.searchclipboardfor.middleclick", false);

    // Built-in password manager and autofill: logins live in Nextcloud
    // Passwords or a security key.
    defaultPref("signon.rememberSignons", false);
    defaultPref("signon.autofillForms", false);
    defaultPref("extensions.formautofill.addresses.enabled", false);
    defaultPref("extensions.formautofill.creditCards.enabled", false);

    // Machine learning features
    defaultPref("browser.ml.enable", false);
    defaultPref("browser.ml.chat.enabled", false);
    defaultPref("browser.ml.chat.menu", false);
    defaultPref("browser.tabs.groups.smart.enabled", false);
    defaultPref("extensions.ui.mlmodel.hidden", true);

    // Start page, onboarding and promotions
    defaultPref("browser.startup.homepage_override.mstone", "ignore");
    defaultPref("startup.homepage_welcome_url", "about:blank");
    defaultPref("startup.homepage_welcome_url.additional", "");
    defaultPref("browser.aboutConfig.showWarning", false);
    defaultPref("browser.newtabpage.activity-stream.feeds.weatherfeed", false);
    defaultPref("browser.newtabpage.activity-stream.showWebNotifications", false);
    defaultPref("browser.newtabpage.activity-stream.section.highlights.includeDownloads", false);
    defaultPref("browser.newtabpage.activity-stream.section.highlights.includeVisited", false);
    defaultPref("extensions.htmlaboutaddons.recommendations.enabled", false);
    defaultPref("extensions.getAddons.showPane", false);
    defaultPref("browser.topsites.contile.enabled", false);
    defaultPref("browser.topsites.useRemoteSetting", false);
    lockPref("browser.newtabpage.activity-stream.default.sites", "");
    lockPref("browser.newtabpage.activity-stream.feeds.telemetry", false);
    lockPref("browser.newtabpage.activity-stream.telemetry", false);
    lockPref("browser.newtabpage.activity-stream.unifiedAds.tiles.enabled", false);
    lockPref("browser.newtabpage.activity-stream.unifiedAds.spocs.enabled", false);
    lockPref("browser.newtabpage.activity-stream.unifiedAds.endpoint", "");
    lockPref("browser.newtabpage.activity-stream.asrouter.providers.cfr", "null");
    lockPref("browser.newtabpage.activity-stream.asrouter.providers.message-groups", "null");
    lockPref("browser.newtabpage.activity-stream.asrouter.providers.messaging-experiments", "null");
    lockPref("browser.newtabpage.activity-stream.asrouter.providers.onboarding", "null");
    lockPref("browser.uitour.enabled", false);
    lockPref("browser.vpn_promo.enabled", false);
    lockPref("browser.promo.focus.enabled", false);
    lockPref("browser.contentblocking.report.hide_vpn_banner", true);
    lockPref("browser.contentblocking.report.show_mobile_app", false);
    lockPref("termsofuse.bypassNotification", true);
    lockPref("datareporting.policy.dataSubmissionPolicyBypassNotification", true);
    lockPref("extensions.webcompat-reporter.enabled", false);

    // Telemetry, studies and crash reports
    lockPref("toolkit.telemetry.unified", false);
    lockPref("toolkit.telemetry.enabled", false);
    lockPref("toolkit.telemetry.server", "data:,");
    lockPref("toolkit.telemetry.archive.enabled", false);
    lockPref("toolkit.telemetry.newProfilePing.enabled", false);
    lockPref("toolkit.telemetry.updatePing.enabled", false);
    lockPref("toolkit.telemetry.firstShutdownPing.enabled", false);
    lockPref("toolkit.telemetry.shutdownPingSender.enabled", false);
    lockPref("toolkit.telemetry.bhrPing.enabled", false);
    lockPref("toolkit.coverage.opt-out", true);
    lockPref("toolkit.coverage.endpoint.base", "");
    lockPref("datareporting.healthreport.uploadEnabled", false);
    lockPref("datareporting.policy.dataSubmissionEnabled", false);
    lockPref("datareporting.usage.uploadEnabled", false);
    lockPref("app.normandy.enabled", false);
    lockPref("app.normandy.api_url", "");
    lockPref("app.shield.optoutstudies.enabled", false);
    lockPref("nimbus.rollouts.enabled", false);
    lockPref("browser.discovery.enabled", false);
    lockPref("browser.tabs.crashReporting.sendReport", false);
    lockPref("breakpad.reportURL", "");
    lockPref("dom.private-attribution.submission.enabled", false);
    lockPref("browser.referrals.enabled", false);
  '';

  policies = {
    DisableTelemetry = true;
    DisableFirefoxStudies = true;
    DisablePocket = true;
    DisableFeedbackCommands = true;
    DisableFirefoxAccounts = true;
    DontCheckDefaultBrowser = true;
    NoDefaultBookmarks = true;
    OverrideFirstRunPage = "";
    OverridePostUpdatePage = "";
    FirefoxHome = {
      SponsoredTopSites = false;
      SponsoredPocket = false;
      SponsoredStories = false;
      Stories = false;
      Pocket = false;
      Locked = true;
    };
    FirefoxSuggest = {
      WebSuggestions = false;
      SponsoredSuggestions = false;
      ImproveSuggest = false;
      Locked = true;
    };
    UserMessaging = {
      ExtensionRecommendations = false;
      FeatureRecommendations = false;
      UrlbarInterventions = false;
      SkipOnboarding = true;
      MoreFromMozilla = false;
      FirefoxLabs = false;
      Locked = true;
    };
  };

  package = pkgs.firefox.override {
    extraPrefs = hardening;
    extraPolicies = policies // {
      ExtensionSettings = extensionSettings;
    };
  };

  # Same Firefox and policies, but no add-ons: the profile is fresh, so they
  # would only open their welcome pages.
  loginPackage = pkgs.firefox.override {
    extraPrefs = hardening;
    extraPolicies = policies // {
      ExtensionSettings."*".installation_mode = "blocked";
    };
  };

  # Login windows (see user/browsers/login.nix) get a fresh profile that
  # saves no passwords and is deleted when the login ends.
  loginRecipe = pkgs.writeShellScript "firefox-login" ''
    set -eu
    profile="$1"
    ${pkgs.coreutils}/bin/install -d -m 0700 "$profile"
    ${pkgs.coreutils}/bin/cat >"$profile/user.js" <<'EOF'
    user_pref("signon.rememberSignons", false);
    user_pref("signon.autofillForms", false);
    user_pref("signon.generation.enabled", false);
    user_pref("browser.formfill.enable", false);
    user_pref("browser.sessionstore.resume_from_crash", false);
    EOF
    exec ${lib.getExe loginPackage} \
      --no-remote --profile "$profile" --name "$2" "$3"
  '';
in
{
  stylix.targets.firefox.profileNames = [ profileName ];
  gjallar.loginBrowsers.firefox = loginRecipe;
  stylix.targets.firefox.colorTheme.enable = true;

  programs.firefox = {
    enable = true;
    inherit package;
    profiles.${profileName} = {
      isDefault = true;
      search = {
        force = true;
        default = "ddg";
        privateDefault = "ddg";
      };
      extensions = {
        packages = lib.mkForce [ ];
        # Stylix writes the theme into the add-on's storage file.
        settings."FirefoxColor@mozilla.com".force = true;
      };
      # The profile scope stays trusted: the LibreWolf import copies the
      # user's own add-ons there. Firefox still asks before it enables
      # add-ons that other programs put anywhere else.
      settings."extensions.autoDisableScopes" = 14;
    };
  };

  systemd.user.services.firefox-addons-reconcile = {
    Unit = {
      Description = "Remove Firefox add-ons GjallarOS no longer installs";
      Before = [ "graphical-session.target" ];
    };
    Service = {
      Type = "oneshot";
      ExecStart = "${gjallarctl}/bin/gjallarctl addons reconcile firefox --desired ${desiredAddons} --profile ${profileDir}";
    };
    Install.WantedBy = [ "graphical-session-pre.target" ];
  };
}
