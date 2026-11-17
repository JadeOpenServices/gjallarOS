{ lib, pkgs, ... }:
{
  services.gnome.gnome-keyring.enable = true;

  # secret-tool: store and look up keyring entries from a shell (tokens
  # for CLI tools, the release bot token in docs/maintainers.md).
  environment.systemPackages = [ pkgs.libsecret ];

  security.pam.services = {
    greetd.enableGnomeKeyring = true;
    login.enableGnomeKeyring = true;
    # passwd knows the old password, so the login keyring follows the new
    # one; otherwise the next login leaves it locked and an app asks for
    # the old password. A root reset cannot re-encrypt it.
    passwd = {
      enableGnomeKeyring = true;
      # NixOS marks pam_unix "sufficient", so a good change ends the stack
      # before pam_gnome_keyring runs. Arch (required) and Fedora
      # (substack) both let the keyring module run after it.
      rules.password.unix.control = lib.mkForce "required";
    };
  };
}
