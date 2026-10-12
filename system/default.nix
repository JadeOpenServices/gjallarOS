{
  inputs,
  lib,
  pkgs,
  settings,
  installState,
  ...
}:
{
  imports = [
    ../generated/hardware.nix

    ./hardware/boot.nix
    ./hardware/battery.nix
    ./hardware/sound.nix
    ./hardware/bluetooth.nix
    ./hardware/graphics
    ./hardware/networking.nix
    ./hardware/firmware.nix
    ./hardware/fingerprint.nix
    ./hardware/thunderbolt.nix
    ./hardware/input.nix
    ./hardware/fan-control.nix
    ./hardware/memory.nix

    ./security/firewall.nix
    ./security/local-hardening.nix
    ./security/console-lockdown.nix
    ./security/disk-unlock.nix
    ./security/touch-unlock.nix
    ./security/keyring.nix
    ./maintenance/storage.nix
    ./maintenance/installer-resume.nix
    ./maintenance/binary-cache.nix
    ./services/printing.nix
    ./services/removable-media.nix

    inputs.oddc.nixosModules.default

    ./recovery
    ../themes/lib/common.nix
    ./tools
    ./users/privilege.nix
    ./users/shell.nix
    ../apps/nixos.nix
  ]
  ++ (map (wm: ./wm/${wm}) settings.wms);

  # The installer's answer: only this machine's model, never the catalog.
  oddc.catalog = lib.mkIf (builtins.pathExists ../generated/oddc) ../generated/oddc;

  oddc.device =
    let
      model = settings.oddcModel or "";
    in
    if model == "" then null else model;

  boot.kernelPackages = pkgs.linuxPackages_latest;

  # The KIO drive-usage patch rebuilds KIO and everything above it (Dolphin,
  # Plasma file dialogs) on the device, so it is opt-in.
  nixpkgs.overlays =
    import ../pkgs/lib/overlays.nix
    ++ lib.optional (settings.dolphinDriveUsage or false) (
      import ../pkgs/optional-overlays/kio-device-capacity-text.nix
    );
  nixpkgs.config.allowUnfree = true;

  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];

  networking.hostName = settings.hostname;
  time.timeZone = settings.timezone;
  services.chrony.enable = true;
  # Site router first; the default pool stays as fallback off-site.
  services.chrony.extraConfig = "server 192.168.8.1 iburst prefer";

  i18n.defaultLocale = settings.locale;
  i18n.extraLocaleSettings.LC_ALL = settings.locale;
  # TTYs and the boot PIN/recovery-key prompt use the desktop layout too;
  # otherwise a PIN set in a de session fails at boot on y/z or symbols.
  console.useXkbConfig = true;

  programs.${settings.shell}.enable = true;

  users.users.${settings.username} = {
    isNormalUser = true;
    shell = pkgs.${settings.shell};
    description = settings.username;
    extraGroups = [ "networkmanager" ]
    ++ lib.optionals (!settings.endpointManagedDevice) [ "wheel" ];
    # Set by the installer. NixOS reads it only when it creates the account;
    # a later passwd change stays.
    hashedPasswordFile = lib.mkIf (
      (settings.userPasswordFile or "") != ""
    ) settings.userPasswordFile;
  };

  programs.nix-ld = {
    enable = true;
    libraries = with pkgs; [ stdenv.cc.cc ];
  };

  environment.systemPackages = with pkgs; [
    home-manager
    nix-index
    pciutils
    go-mtpfs
    lsof
    git
    vim
  ];

  services.gvfs.enable = true;

  system.stateVersion = installState.nixosStateVersion;
}
