{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  managed = settings.management.enable;

  gjallarctlPackage = pkgs.callPackage ../../pkgs/gjallarctl { };
  gjallarSudoAuth = "${gjallarctlPackage}/bin/gjallar-sudo-auth";

  jodsEnrollmentAllowed =
    settings.management.fingerprintEnrollmentAllowed or false;

  # ODDC marks readers with no Linux driver (the ZBook x2's VFS495) as
  # support.linux.status = "unsupported". Treat those as no reader, so sudo and
  # the lock screen do not wait on a scan that never comes. A missing support
  # entry makes no claim either way.
  fingerprint = lib.attrByPath [
    "oddc"
    "resolved"
    "hardware"
    "security"
    "fingerprint"
  ] { } config;
  fingerprintPresent =
    fingerprint ? primary
    && lib.attrByPath [ "primary" "support" "linux" "status" ] null fingerprint != "unsupported";

  fingerprintState = pkgs.writeShellScript "gjallar-fingerprint-state" ''
    set -eu

    user="''${1:-}"

    if [ -z "$user" ] ||
       ! ${pkgs.getent}/bin/getent passwd "$user" >/dev/null; then
      echo invalid
      exit 0
    fi

    if [ -d "/var/lib/fprint/$user" ] &&
       ${pkgs.findutils}/bin/find \
         "/var/lib/fprint/$user" \
         -type f -print -quit 2>/dev/null |
       ${pkgs.gnugrep}/bin/grep -q .; then
      echo enrolled
    else
      echo initial
    fi
  '';
in
lib.mkMerge [
  {
    assertions = [
      {
        assertion = !jodsEnrollmentAllowed || managed;
        message =
          "management.fingerprintEnrollmentAllowed is valid only on a JODS-managed device.";
      }
      {
        assertion = !jodsEnrollmentAllowed || fingerprintPresent;
        message =
          "management.fingerprintEnrollmentAllowed requires fingerprint hardware in the resolved ODDC device.";
      }
    ];

    # GjallarOS privilege authentication intentionally splits biometric and
    # password authentication into separate PAM transactions. The first
    # service can never request a Unix password. The second can never invoke
    # the fingerprint module.
    security.pam.services = {
      gjallar-sudo-fingerprint = {
        unixAuth = false;
        fprintAuth = fingerprintPresent;
      };

      gjallar-sudo-password = {
        unixAuth = true;
        fprintAuth = false;
        rules.auth.unix.args = lib.mkForce [ "likeauth" ];
      };
    };

    # Scope the split PAM policy to the immutable no-op helper only. Normal
    # sudo keeps its existing system policy.
    security.sudo.extraConfig = lib.mkAfter ''
      Defaults!${gjallarSudoAuth} pam_service=gjallar-sudo-fingerprint, pam_askpass_service=gjallar-sudo-password
    '';
  }

  (lib.mkIf fingerprintPresent {
    services.fprintd.enable = true;

    security.pam.services = {
      greetd.fprintAuth = false;
      hyprlock.fprintAuth = true;
      login.fprintAuth = false;

      sudo = {
        fprintAuth = true;

        rules.auth.unix.args = lib.mkForce [ "likeauth" ];
      };

      polkit-1 = {
        fprintAuth = false;
        rules.auth.unix.args = lib.mkForce [ "likeauth" ];
      };

      swaylock.fprintAuth = true;
    };

    security.polkit.extraConfig = lib.mkBefore ''
      polkit.addRule(function(action, subject) {
        if (action.id != "net.reactivated.fprint.device.enroll") {
          return polkit.Result.NOT_HANDLED;
        }

        if (!subject.active || !subject.local) {
          return polkit.Result.NO;
        }

        if (${if managed then "true" else "false"}) {
          // JODS owns enrollment policy. A local UI cannot grant itself access.
          if (!${if jodsEnrollmentAllowed then "true" else "false"}) {
            return polkit.Result.NO;
          }

          return polkit.Result.AUTH_ADMIN;
        }

        var state =
          polkit.spawn(["${fingerprintState}", subject.user]);

        if (state.trim() == "enrolled") {
          // Do not use *_KEEP: every later add/delete gets a fresh admin prompt.
          return polkit.Result.AUTH_ADMIN;
        }

        return polkit.Result.AUTH_SELF;
      });
    '';
  })
]
