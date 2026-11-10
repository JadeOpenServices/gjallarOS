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

  managedEnrollmentAllowed =
    settings.management.fingerprintEnrollmentAllowed or false;

  # ODDC marks readers with no Linux driver (the ZBook x2's VFS495) as
  # support.linux.status = "unsupported". Treat those as no reader, so the lock
  # screen does not wait on a scan that never comes. A missing support entry
  # makes no claim either way.
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
{
  # A finger counts only on the lock screens (Q216 step 1, loophole 46).
  # pam_fprintd alone cannot tell which action a touch approves: any program
  # running as the user can start sudo, su or run0 and take a touch meant for
  # something else. NixOS turns the finger on for every PAM service once
  # fprintd runs; this flips that default, so services added later stay
  # password-only too. Privileged actions get the finger back only through a
  # root service that shows a dialog naming the action (Q216 step 2).
  options.security.pam.services = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule {
        config.fprintAuth = lib.mkDefault false;
      }
    );
  };

  config = lib.mkMerge [
    {
      assertions = [
        {
          assertion = !managedEnrollmentAllowed || managed;
          message =
            "management.fingerprintEnrollmentAllowed is valid only on a managed device.";
        }
        {
          assertion = !managedEnrollmentAllowed || fingerprintPresent;
          message =
            "management.fingerprintEnrollmentAllowed requires fingerprint hardware in the resolved ODDC device.";
        }
      ];

      # gjallarctl authenticates through the no-op helper with sudo -A. This
      # service only ever asks for the Unix password.
      security.pam.services.gjallar-sudo-password = {
        unixAuth = true;
        rules.auth.unix.args = lib.mkForce [ "likeauth" ];
      };

      # Scope the password-only service to the immutable helper. Normal sudo
      # keeps its own policy.
      security.sudo.extraConfig = lib.mkAfter ''
        Defaults!${gjallarSudoAuth} pam_askpass_service=gjallar-sudo-password
      '';
    }

    (lib.mkIf fingerprintPresent {
      services.fprintd.enable = true;

      security.pam.services = {
        hyprlock.fprintAuth = true;
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
            // The management server owns enrollment policy. A local UI cannot grant itself access.
            if (!${if managedEnrollmentAllowed then "true" else "false"}) {
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
  ];
}
