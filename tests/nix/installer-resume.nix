{
  nixpkgs,
  system,
  resumeModule,
  # Written by internal/installer/resume.Module for release alignment.
  wrapperModule,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};
  evaluate =
    modules:
    nixpkgs.lib.nixosSystem {
      inherit system;
      modules = modules ++ [
        {
          system.stateVersion = "26.05";
          boot.loader.grub.enable = false;
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "ext4";
          };
        }
      ];
    };
  unitOf = evaluated: evaluated.config.systemd.units."installer-resume.service".text;
  permanent = unitOf (evaluate [ resumeModule ]);
  # The wrapper adds its unit to a configuration that may already be
  # gjallarOS; both copies together must not conflict.
  both = unitOf (evaluate [
    resumeModule
    wrapperModule
  ]);
  wrapperOnly = unitOf (evaluate [ wrapperModule ]);
  has = needle: text: nixpkgs.lib.hasInfix needle text;
in
# A reboot mid-resume lost the transaction: the unit moved pending.json
# away before the run and was absent from the installed gjallarOS
# generation (e2e-target, 2026-10-05).
assert has "ConditionPathExists=|/var/lib/gjallarOS/installer-resume/pending.json" permanent;
assert has "ConditionPathExists=|/var/lib/gjallarOS/installer-resume/active.json" permanent;
assert !(has "mv " permanent);
# The continuation prompts; it needs tty1 before the greeter takes it.
assert has "StandardInput=tty" permanent;
assert has "StandardOutput=tty" permanent;
assert has "TTYPath=/dev/tty1" permanent;
assert has "Before=display-manager.service greetd.service" permanent;
# The installed system does not ship the bootstrap-only tools.
assert has "-sbctl-" permanent;
assert has "ExecStart=/var/lib/gjallarOS/installer-resume/gjallar-installer --resume-transaction /var/lib/gjallarOS/installer-resume\n" permanent;
assert builtins.elem "multi-user.target" (evaluate [ resumeModule ]).config.systemd.services.installer-resume.wantedBy;
assert both == permanent;
assert wrapperOnly == permanent;
pkgs.runCommand "gjallar-installer-resume-check" { } "touch $out"
