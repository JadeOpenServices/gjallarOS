{
  config,
  inputs,
  lib,
  pkgs,
  settings,
  ...
}:
let
  cfg = config.services.jods-mdm-agent;
  enabled = settings.management.enable or false;
  statusWriter = pkgs.writeShellScript "gjallar-jods-status" ''
    set -eu
    install -d -m 0700 /var/lib/gjallarOS
    response=/var/lib/jods-mdm-agent/bootstrap-response.json
    state='enrollment submitted'
    result="''${SERVICE_RESULT:-success}"
    code="''${EXIT_STATUS:-0}"
    if [ "$result" != success ]; then
      case "$code" in
        15|16|17|18|20|21|22|23) state='policy verification failed' ;;
        11|12) state='enrollment blocked' ;;
        *) state='enrollment blocked' ;;
      esac
    fi
    if [ "$result" = success ] && [ -s "$response" ]; then
      remote="$(${lib.getExe pkgs.jq} -r '.data.status // .data.mode // empty' "$response" 2>/dev/null || true)"
      case "$remote" in
        approved|enrolled) state=enrolled ;;
        pending|awaiting-approval) state='awaiting approval' ;;
        blocked) state='enrollment blocked' ;;
      esac
    fi
    printf '%s\n' "$state" > /var/lib/gjallarOS/jods-status.txt
    chmod 0644 /var/lib/gjallarOS/jods-status.txt
  '';
  statusCommand = pkgs.writeShellApplication {
    name = "gjallar-jods-status";
    runtimeInputs = [ pkgs.jq ];
    text = ''
      set -euo pipefail
      status=/var/lib/gjallarOS/jods-status.txt
      response=/var/lib/jods-mdm-agent/bootstrap-response.json
      if [ -r "$status" ]; then
        printf 'JODS: '
        cat "$status"
      else
        printf '%s\n' 'JODS: configured, not contacted'
      fi
      if [ -r "$response" ]; then
        jq '{status: .data.status, mode: .data.mode, device_id: .data.device_id, message: (.message // .data.message)}' "$response"
      fi
    '';
  };
in
{
  imports = [ ];

  services.jods-mdm-agent = lib.mkIf enabled {
    enable = true;
    endpoint = settings.management.endpoint;
    policySigningPublicKey = settings.management.policySigningPublicKey;
    recoveryCommandSigningPublicKey = settings.management.recoveryCommandSigningPublicKey;
    managedEnrollmentConsent = true;
    ownershipModel = "corporate";
    recoveryCheckpointEnable = settings.recoveryEnable;
    enrollmentMode = settings.management.enrollmentMode;
    allowInsecureTls = settings.management.allowInsecureTls;
    deviceClass = settings.management.deviceClass;
    desktopProfile = settings.management.desktopProfile;
  };
  environment.systemPackages = lib.optionals enabled [ statusCommand ];

  systemd.services.jods-mdm-agent-enroll = lib.mkIf enabled {
    wantedBy = lib.mkForce [ ];
    unitConfig = {
      ConditionPathExists = "/var/lib/gjallarOS/installation-complete";
      ConditionPathExistsGlob = "!/var/lib/gjallarOS/jods-enrollment-complete";
    };
    serviceConfig = {
      UMask = "0077";
      ExecStopPost = statusWriter;
      ExecStartPost = pkgs.writeShellScript "gjallar-jods-mark-enrolled" ''
        response=/var/lib/jods-mdm-agent/bootstrap-response.json
        marker=/var/lib/gjallarOS/jods-enrollment-complete

        if [ -s "$response" ]; then
          status="$(${pkgs.jq}/bin/jq -r '.data.status // empty' "$response" 2>/dev/null || true)"
          mode="$(${pkgs.jq}/bin/jq -r '.data.mode // empty' "$response" 2>/dev/null || true)"
          device_id="$(${pkgs.jq}/bin/jq -r '.data.device_id // empty' "$response" 2>/dev/null || true)"

          if { [ "$status" = approved ] || [ "$mode" = reenrolled ]; } && [ -n "$device_id" ]; then
            ${pkgs.coreutils}/bin/touch "$marker"
            ${pkgs.coreutils}/bin/chmod 0600 "$marker"
          fi
        fi
      '';

    };
  };
  systemd.timers.jods-mdm-agent-enroll.wantedBy = lib.mkIf enabled (lib.mkForce [ ]);

  systemd.tmpfiles.rules = lib.optionals enabled [
    "d /var/lib/jods-mdm-agent 0700 root root - -"
    "d /var/lib/gjallarOS 0700 root root - -"
  ];

  assertions = lib.optionals enabled [
    {
      assertion = builtins.match "https://.+" settings.management.endpoint != null;
      message = "Managed JODS endpoints must use HTTPS.";
    }
    {
      assertion = builtins.match "[0-9a-fA-F]{64}" settings.management.policySigningPublicKey != null;
      message = "JODS policySigningPublicKey must contain exactly 64 hexadecimal characters.";
    }
  ];
}
