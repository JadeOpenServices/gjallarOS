{
  buildGoModule,
  dbus,
  git,
  lib,
  runtimeShell,
  systemd,
}:
buildGoModule {
  pname = "gjallarctl";
  version = "0.1.0";

  nativeCheckInputs = [
    dbus
    git
  ];

  src = lib.fileset.toSource {
    root = ../../.;
    fileset = lib.fileset.unions [
      ../../go.mod
      ../../go.sum
      (lib.fileset.fileFilter (f: f.hasExt "go" || f.name == "meta.json") ../../apps)
      ../../apps/firefox-addons.json
      ../../cmd
      ../../internal/ai
      ../../internal/binarycache
      ../../internal/gittest
      ../../internal/hardware
      ../../internal/input
      ../../internal/installercheck
      ../../internal/installer
      ../../internal/localchanges
      ../../internal/managedapps
      ../../internal/nettrust
      ../../internal/noctalia
      ../../internal/oddccli
      ../../internal/preset
      ../../internal/settings
      ../../internal/sshsig
      ../../internal/usbtrust
    ];
  };

  vendorHash = "sha256-Vt+uu24eoM6iJNbf1jUJePCghiTlQpy2q4CQOLQZN/g=";
  # Module mode: ODDC's catalog files stay in the module cache, which the
  # tests read as the real catalog.
  proxyVendor = true;

  # The installer and recovery maintenance live in pkgs/gjallar-installer,
  # which the installed system does not carry.
  subPackages = [ "cmd/gjallarctl" ];

  env.CGO_ENABLED = "0";
  ldflags = [
    "-s"
    "-w"
  ];

  postInstall = ''
    cat > "$out/bin/gjallar-sudo-askpass" <<'ASKPASS'
#!${runtimeShell}
# sudo runs askpass with its own stdin, which gjallarctl detaches. Prompt on
# the controlling terminal; without one, fail instead of waiting forever for
# a password agent that does not exist. Give up after 5 minutes like sudo's
# own passwd_timeout, so an unattended prompt does not wait all night.
exec ${lib.getExe' systemd "systemd-ask-password"} \
  --echo=no \
  --timeout=300 \
  "$@" </dev/tty
ASKPASS
    chmod 0555 "$out/bin/gjallar-sudo-askpass"

    cat > "$out/bin/gjallar-sudo-auth" <<'AUTH'
#!${runtimeShell}
exit 0
AUTH
    chmod 0555 "$out/bin/gjallar-sudo-auth"

    install -Dm444 ${./_gjallarctl} "$out/share/zsh/site-functions/_gjallarctl"
  '';

  meta = {
    description = "Safe GjallarOS maintenance and validation tool";
    mainProgram = "gjallarctl";
    license = lib.licenses.agpl3Plus;
    platforms = lib.platforms.linux;
  };
}
