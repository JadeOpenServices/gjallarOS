{ lib, ... }:
{
  # Laptops join untrusted networks: never let ICMP redirects or source routes
  # change the routing table, and never send redirects ourselves.
  boot.kernel.sysctl = {
    "net.ipv4.conf.all.accept_redirects" = 0;
    "net.ipv4.conf.default.accept_redirects" = 0;
    "net.ipv4.conf.all.secure_redirects" = 0;
    "net.ipv4.conf.default.secure_redirects" = 0;
    "net.ipv4.conf.all.send_redirects" = 0;
    "net.ipv4.conf.default.send_redirects" = 0;
    "net.ipv4.conf.all.accept_source_route" = 0;
    "net.ipv6.conf.all.accept_redirects" = 0;
    "net.ipv6.conf.default.accept_redirects" = 0;
    "net.ipv6.conf.all.accept_source_route" = 0;
  };

  # Stored account hashes: root-only directory, so the set of accounts with
  # stored hashes is not listable. Also tightens directories created 0755 by
  # earlier installer versions.
  systemd.tmpfiles.rules = [
    "d /var/lib/gjallarOS/passwords 0700 root root -"
  ];

  # nixpkgs lets an account with an empty password field log in at the
  # greeter and on the console (pam_unix nullok). Every account here has a
  # password; an emptied hash must lock the account, not open it.
  security.pam.services.greetd.allowNullPassword = lib.mkForce false;
  security.pam.services.login.allowNullPassword = lib.mkForce false;

  # pkexec's generic action runs any program as root behind one password
  # dialog, so every local process can raise a root prompt that looks like a
  # gjallarOS one. Tools that need root ship their own action with a fixed
  # program path (exec.path annotation), as KDE does with KAuth helpers; the
  # generic action is refused. Root keeps it for recovery work.
  security.polkit.extraConfig = lib.mkAfter ''
    polkit.addRule(function(action, subject) {
      if (action.id == "org.freedesktop.policykit.exec" && subject.user != "root") {
        return polkit.Result.NO;
      }
      return polkit.Result.NOT_HANDLED;
    });
  '';
}
