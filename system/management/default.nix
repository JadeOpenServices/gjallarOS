{
  config,
  inputs,
  lib,
  pkgs,
  settings,
  ...
}:
let
  cfg = config.services.mdm-agent;
  libexec = import ../../pkgs/lib/libexec.nix { inherit lib pkgs; };
  # Device contract versions this gjallarOS implements (JODS-567). A JODS
  # pin with another version stops the build instead of running an agent
  # whose names or recovery steps no longer match.
  supportedContractVersions = [ 2 ];
  # What the agent reports as the gjallarOS release: the NixOS release and
  # the commit the generation was built from, when there is one.
  revision = builtins.match "(git:)?([0-9a-f]{40}(-dirty)?)" config.system.configurationRevision;
  osRelease =
    config.system.nixos.release
    + lib.optionalString (revision != null) "+${builtins.elemAt revision 1}";
  statusWriter = pkgs.writeShellScript "gjallar-management-status" ''
    set -eu
    install -d -m 0700 /var/lib/gjallarOS
    response=/var/lib/mdm-agent/bootstrap-response.json
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
    printf '%s\n' "$state" > /var/lib/gjallarOS/management-status.txt
    chmod 0644 /var/lib/gjallarOS/management-status.txt
  '';
  statusCommand = pkgs.writeShellApplication {
    name = "gjallar-management-status";
    runtimeInputs = [ pkgs.jq ];
    text = ''
      set -euo pipefail
      status=/var/lib/gjallarOS/management-status.txt
      response=/var/lib/mdm-agent/bootstrap-response.json
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
  services.mdm-agent = {
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
    inherit osRelease;
  };
  # gjallarctl management status starts it (Plane 282); not on PATH.
  environment.systemPackages = [ (libexec statusCommand) ];

  systemd.services.mdm-agent-enroll = {
    wantedBy = lib.mkForce [ ];
    unitConfig = {
      ConditionPathExists = "/var/lib/gjallarOS/installation-complete";
      ConditionPathExistsGlob = "!/var/lib/gjallarOS/management-enrollment-complete";
    };
    serviceConfig = {
      UMask = "0077";
      ExecStopPost = statusWriter;
      ExecStartPost = pkgs.writeShellScript "gjallar-management-mark-enrolled" ''
        response=/var/lib/mdm-agent/bootstrap-response.json
        marker=/var/lib/gjallarOS/management-enrollment-complete

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
  systemd.timers.mdm-agent-enroll.wantedBy = lib.mkForce [ ];

  systemd.tmpfiles.rules = [
    "d /var/lib/mdm-agent 0700 root root - -"
    "d /var/lib/gjallarOS 0700 root root - -"
  ];

  assertions = [
    {
      assertion = lib.elem cfg.contract.version supportedContractVersions;
      message = "The pinned JODS agent implements device contract version ${toString cfg.contract.version}; this gjallarOS supports ${
        lib.concatMapStringsSep ", " toString supportedContractVersions
      }. Pin a JODS commit with a supported contract, or update gjallarOS.";
    }
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
