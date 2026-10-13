{
  description = "GjallarOS — a practical Nordic NixOS workstation";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";

    home-manager.url = "github:nix-community/home-manager/release-26.05";
    home-manager.inputs.nixpkgs.follows = "nixpkgs";

    lanzaboote = {
      url = "github:nix-community/lanzaboote/v1.1.0";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    sops-nix.url = "github:Mic92/sops-nix";

    nur = {
      url = "github:nix-community/NUR";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    stylix = {
      url = "github:danth/stylix/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    caelestia.url = "github:caelestia-dots/shell";

    noctalia-greeter = {
      url = "github:noctalia-dev/noctalia-greeter";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    noctalia = {
      url = "github:noctalia-dev/noctalia";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Module code only; the machine's own model is the answer in
    # generated/oddc, fetched at the commit this input is locked to.
    oddc = {
      url = "github:JadeOpenServices/oddc";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    jods = {
      url = "git+https://github.com/JadeOpenServices/jods.git?rev=762d016f36f60225e592c0961a38a4d8352997f3&shallow=1";
      flake = false;
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      home-manager,
      ...
    }@inputs:
    let
      system = "x86_64-linux";
      lib = nixpkgs.lib;
      releasePolicy = builtins.fromJSON (builtins.readFile ./deployment/release-policy.json);
      sourceRevision =
        if self ? rev then
          "git:${self.rev}"
        else if self ? dirtyRev then
          "git:${self.dirtyRev}"
        else if self ? narHash then
          "nar:${self.narHash}"
        else
          throw "GjallarOS source provenance is unavailable";

      overlays = [
        inputs.nur.overlays.default
      ]
      ++ import ./pkgs/lib/overlays.nix;

      mkPkgs =
        targetSystem:
        import nixpkgs {
          system = targetSystem;
          inherit overlays;
          config.allowUnfree = true;
        };

      basePkgs = mkPkgs system;

      # Firefox replaced LibreWolf. Installs that still list LibreWolf get
      # Firefox as well and open links in it. LibreWolf stays until it is
      # removed from user.config.json, so its profile can be imported.
      stateSettings = import (./. + "/generated/state.nix") {
        pkgs = basePkgs;
        inherit inputs;
      };
      settings =
        stateSettings
        // lib.optionalAttrs (builtins.elem "librewolf" stateSettings.browsers) {
          browsers = [ "firefox" ] ++ builtins.filter (b: b != "firefox") stateSettings.browsers;
          preferredBrowser =
            if stateSettings.preferredBrowser == "librewolf" then "firefox" else stateSettings.preferredBrowser;
        };

      installState = import (./. + "/generated/install-state.nix");

      pkgs = basePkgs;
    in
    {
      packages.${system} = rec {
        gjallarctl = pkgs.callPackage ./pkgs/gjallarctl { };
        "gjallar-usbtrustd" = pkgs.callPackage ./pkgs/gjallar-usbtrustd { };
        "gjallar-recovery-install" = pkgs.callPackage ./pkgs/gjallar-recovery-install { };
        "gjallar-installer" = pkgs.callPackage ./pkgs/gjallar-installer { inherit (inputs) oddc; };
        "gjallar-recovery-iso" = self.nixosConfigurations.gjallar-recovery.config.system.build.isoImage;
        "gjallar-installer-lab-iso" =
          self.nixosConfigurations.gjallar-installer-lab.config.system.build.isoImage;

        # Packages that cache.nixos.org does not have because GjallarOS patches
        # them. The modules use the same files with the same pkgs, so a build
        # here has the drvPath the machines need. CI builds `prebuilt` for each
        # release tag and pushes it to the cache in deployment/binary-cache.json.
        noctalia = pkgs.callPackage ./pkgs/noctalia {
          noctalia = inputs.noctalia.packages.${system}.default;
        };
        noctalia-greeter = pkgs.callPackage ./pkgs/noctalia-greeter {
          noctalia-greeter = inputs.noctalia-greeter.packages.${system}.default;
        };
        monique = pkgs.callPackage ./pkgs/monique/nix/package.nix { };
        "nextcloud-client-openvfs" = (import ./apps/nextcloud/package.nix { inherit pkgs; }).client;
        openvfs = (import ./apps/nextcloud/package.nix { inherit pkgs; }).openvfs;
        "gjallar-opencode" = import ./apps/ai/opencode.nix { inherit pkgs; };
        inherit (pkgs.qt6Packages) qt6ct;
        inherit (pkgs.kdePackages) dolphin;
        inherit (pkgs)
          _nemu
          ghostty
          hyprland
          usbguard
          wvkbd
          xdg-desktop-portal-hyprland
          ;

        prebuilt = pkgs.linkFarmFromDrvs "gjallar-prebuilt" [
          gjallarctl
          gjallar-usbtrustd
          noctalia
          noctalia-greeter
          monique
          nextcloud-client-openvfs
          openvfs
          gjallar-opencode
          qt6ct
          _nemu
          # pkgs/overlays patches these, so cache.nixos.org has other builds.
          dolphin
          ghostty
          hyprland
          usbguard
          wvkbd
          xdg-desktop-portal-hyprland
        ];
      };

      checks.${system} = {
        build-entrypoint = import ./tests/nix/build-entrypoint.nix { inherit pkgs; };

        nextcloud-client-patches = import ./tests/nix/nextcloud-client-patches.nix { inherit pkgs; };

        security-baseline = import ./tests/nix/security-baseline.nix {
          inherit nixpkgs system;
          bootModule = ./system/hardware/boot.nix;
          hardeningModule = ./system/security/local-hardening.nix;
        };

        measured-boot-limit = import ./tests/nix/measured-boot-limit.nix {
          inherit nixpkgs system;
          lanzabooteModule = inputs.lanzaboote.nixosModules.lanzaboote;
          secureBootModule = ./system/security/secure-boot/lanzaboote.nix;
          flashReadModule = ./system/security/secure-boot/flash-read.nix;
        };

        secure-boot-lifecycle = import ./tests/nix/secure-boot-lifecycle.nix {
          inherit nixpkgs system;
          lifecycleModule = ./system/security/secure-boot/lifecycle.nix;
          measuredBootModule = ./system/security/secure-boot/measured-boot.nix;
          dbxUpdateModule = ./system/security/secure-boot/dbx-update.nix;
        };

        installer-resume = import ./tests/nix/installer-resume.nix {
          inherit nixpkgs system;
          resumeModule = ./system/maintenance/installer-resume.nix;
          wrapperModule = ./tests/nix/installer-resume-wrapper.nix;
        };

        avahi-daemon-fix = import ./tests/nix/avahi-daemon-fix.nix {
          inherit pkgs;
          fixModule = ./system/fixes/avahi-daemon;
        };

        tailscale-trust = import ./tests/nix/tailscale-trust.nix {
          inherit nixpkgs system;
          tailscaleModules = [
            ./apps/tailscale/nixos.nix
            ./apps/nixos-options.nix
          ];
        };

        oddc-security-flags = import ./tests/nix/oddc-security-flags.nix {
          inherit nixpkgs system;
          inherit (inputs) oddc;
          gjallarctlSource = ./cmd/gjallarctl;
        };

        recovery-maintenance = import ./tests/nix/recovery-maintenance.nix {
          inherit nixpkgs system;
          recoveryModule = ./system/recovery;
          sources = [
            ./system
            ./scripts
          ];
        };
      };

      formatter = {

        x86_64-linux = nixpkgs.legacyPackages.x86_64-linux.nixfmt;

      };

      nixosConfigurations = {
        gjallar-recovery = nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [ ./system/recovery/image.nix ];
          specialArgs = {
            releaseVersion = releasePolicy.release;
            repoSource = self.outPath;
            inherit settings sourceRevision;
          };
        };

        gjallar-installer-lab = nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            ./system/recovery/image.nix
            ./system/recovery/installer-lab.nix
          ];
          specialArgs = {
            releaseVersion = releasePolicy.release;
            repoSource = self.outPath;
            inherit settings sourceRevision;
          };
        };

        ${settings.hostname} = nixpkgs.lib.nixosSystem {
          modules = [
            (import ./system/security/build-entrypoint.nix {
              buildContext =
                if builtins.pathExists (self.outPath + "/.gjallar-build-context.json") then
                  builtins.fromJSON (builtins.readFile (self.outPath + "/.gjallar-build-context.json"))
                else
                  null;
            })
            ./system/security/secure-boot
            inputs.noctalia-greeter.nixosModules.default
            inputs.stylix.nixosModules.stylix
            inputs.sops-nix.nixosModules.sops
            inputs.home-manager.nixosModules.home-manager
            ./pkgs/monique/nix/nixos-module.nix
            inputs.lanzaboote.nixosModules.lanzaboote

            {
              nixpkgs.overlays = [
                inputs.nur.overlays.default
              ];

              home-manager.useGlobalPkgs = true;
              home-manager.useUserPackages = true;
              home-manager.backupFileExtension = "hm-bak";

              home-manager.extraSpecialArgs = {
                inherit inputs settings installState;
              };

              home-manager.sharedModules = [
                inputs.sops-nix.homeManagerModules.sops
                inputs.noctalia.homeModules.default
              ];

              home-manager.users = {
                ${settings.username} = import ./user/default.nix;
              };

              systemd.services.display-manager.after = [
                "home-manager-${settings.username}.service"
              ];

              systemd.services.display-manager.wants = [
                "home-manager-${settings.username}.service"
              ];

              systemd.services.display-manager.requires = [
                "home-manager-${settings.username}.service"
              ];
            }

            ./system/default.nix
          ]
          ++ nixpkgs.lib.optionals (settings.endpointManagedDevice or false) [
            ./system/management/jods
            "${inputs.jods}/nix/modules/jods-mdm-agent.nix"
          ];

          specialArgs = {
            inherit
              inputs
              settings
              sourceRevision
              installState
              ;
          };
        };
      };

      homeConfigurations = {
        ${settings.username} = home-manager.lib.homeManagerConfiguration {
          pkgs = mkPkgs settings.system;

          modules = [
            ./user/default.nix
            inputs.stylix.homeModules.stylix
            inputs.sops-nix.homeManagerModules.sops
            inputs.noctalia.homeModules.default
          ];

          extraSpecialArgs = {
            inherit inputs settings installState;
          };
        };
      };
    };
}
