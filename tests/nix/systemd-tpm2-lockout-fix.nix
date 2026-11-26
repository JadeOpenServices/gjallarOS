# system/fixes/systemd (tpm2-setup-lockout.patch) next to a stock systemd
# (Plane 333).
# The VM's software TPM is put into dictionary attack lockout, then both
# systemd-tpm2-setup binaries try to anchor the cryptsetup NvPCR. Here the
# TPM refuses to unseal the stored anchor secret, so stock exits 1 ("Failed to
# acquire anchor secret: No locks available"). On the T480s it failed one
# step later, at the extend ("Failed to extend NvPCR index with anchor
# secret"). Two boot cases follow, with no anchor secret in /run yet: the
# secret comes from a copy passed in as from the ESP, which the TPM refuses
# to decrypt, or there is no copy and a first one has to be sealed. Stock
# makes up a new secret in both and fails to seal it ("State not
# recoverable"). The fixed one skips the NvPCR with a notice in all cases,
# keeps the stored copies and exits EX_TEMPFAIL (75), which its unit accepts.
# After the lockout is cleared the fixed one anchors the NvPCR. When stock
# stops failing, the test says to drop the fix.
{ pkgs, fixedSystemd }:

pkgs.testers.runNixOSTest {
  name = "systemd-tpm2-lockout-fix";

  nodes.machine = {
    virtualisation.tpm.enable = true;
    virtualisation.memorySize = 1024;
    environment.systemPackages = [ pkgs.tpm2-tools ];
    environment.variables.TPM2TOOLS_TCTI = "device:/dev/tpmrm0";
    # The NvPCR gjallarOS sets up (system/security/secure-boot/measured-boot.nix).
    environment.etc."nvpcr/cryptsetup.nvpcr".source = "${pkgs.systemd}/lib/nvpcr/cryptsetup.nvpcr";
  };

  testScript = ''
    stock = "${pkgs.systemd}/lib/systemd/systemd-tpm2-setup"
    fixed = "${fixedSystemd}/lib/systemd/systemd-tpm2-setup"
    assert stock != fixed, "fixedSystemd is the stock systemd"
    anchor = "/run/systemd/nvpcr/cryptsetup.anchor"
    secret = "/run/systemd/nvpcr/nvpcr-anchor.cred"
    var_secret = "/var/lib/systemd/nvpcr/nvpcr-anchor.cred"
    # A DA-protected NV index with a known password, to spend the one try.
    index = "0x01500020"


    # No --graceful: the test VM boots without UEFI, so the firmware part of
    # the TPM2 support check fails and --graceful would exit 0 before any
    # NvPCR work. The units pass --graceful on real machines.
    def setup(binary, env=""):
        return machine.execute(f"{env} {binary} 2>&1")


    def stock_fails(rc, out, *reasons):
        if rc == 0:
            raise Exception(
                "stock systemd-tpm2-setup now passes while the TPM is in"
                " dictionary attack lockout; drop tpm2-setup-lockout.patch from"
                " system/fixes/systemd"
            )
        assert any(r in out for r in reasons), "stock failed for another reason"


    def in_lockout():
        out = machine.succeed("tpm2_getcap properties-variable")
        return any(l.split() == ["inLockout:", "1"] for l in out.splitlines())


    machine.wait_for_unit("multi-user.target")
    machine.wait_for_file("/dev/tpmrm0")

    with subtest("both units accept EX_TEMPFAIL in the fixed systemd"):
        # nixpkgs installs systemd's own unit files under example/.
        for unit in ["systemd-tpm2-setup.service", "systemd-tpm2-setup-early.service"]:
            machine.succeed(f"grep -qx 'SuccessExitStatus=TEMPFAIL' ${fixedSystemd}/example/systemd/system/{unit}")

    with subtest("stock tpm2-setup anchors the NvPCR on an unlocked TPM"):
        rc, out = setup(stock)
        print(out)
        assert rc == 0, f"stock tpm2-setup failed on an unlocked TPM ({rc})"
        machine.succeed(f"test -e {anchor}")
        # A new boot: the NvPCR needs its anchor again.
        machine.succeed(f"rm {anchor}")

    with subtest("put the TPM into dictionary attack lockout"):
        machine.succeed(
            "tpm2_dictionarylockout --setup-parameters --max-tries=1"
            " --recovery-time=3600 --lockout-recovery-time=3600"
        )
        machine.succeed(
            f"tpm2_nvdefine {index} -C o -s 8"
            " -a 'ownerread|ownerwrite|authread|authwrite' -p good"
        )
        machine.succeed(f"printf 12345678 | tpm2_nvwrite {index} -C o -i-")
        machine.fail(f"tpm2_nvread {index} -C {index} -P bad -s 8")
        assert in_lockout(), "the TPM did not enter lockout"

    with subtest("fixed tpm2-setup skips the NvPCR and exits EX_TEMPFAIL"):
        rc, out = setup(fixed)
        print(out)
        assert rc == 75, f"fixed tpm2-setup exited {rc}, expected 75"
        assert "dictionary attack lockout" in out, "no lockout notice"
        machine.fail(f"test -e {anchor}")
        machine.succeed(f"systemd-run --wait --quiet -p SuccessExitStatus=TEMPFAIL {fixed}")

    with subtest("stock tpm2-setup fails in lockout"):
        rc, out = setup(stock)
        print(out)
        stock_fails(
            rc, out,
            "Failed to acquire anchor secret: No locks available",
            "Failed to extend NvPCR index with anchor secret",
        )
        machine.fail(f"test -e {anchor}")

    # A new boot: no anchor secret in /run yet. The boot loader passes the
    # ESP copy in as an encrypted system credential; the test VM has no ESP,
    # so a copy of the /var one stands in for it.
    machine.succeed(f"mv {secret} /root/anchor-secret.saved")
    machine.succeed(f"mkdir /root/creds && cp {var_secret} /root/creds/nvpcr-anchor.test.cred")
    stored = machine.succeed(f"sha256sum {var_secret} /root/creds/nvpcr-anchor.test.cred")
    passed = "SYSTEMD_ENCRYPTED_SYSTEM_CREDENTIALS_DIRECTORY=/root/creds"

    for case, env in [("the copy passed in as from the ESP", passed), ("a first secret", "")]:
        with subtest(f"boot in lockout, {case}: stock fails, fixed exits EX_TEMPFAIL"):
            machine.succeed(f"rm -f {secret}")
            rc, out = setup(stock, env)
            print(out)
            stock_fails(rc, out, "Failed to acquire anchor secret: State not recoverable")
            machine.succeed(f"rm -f {secret}")
            rc, out = setup(fixed, env)
            print(out)
            assert rc == 75, f"fixed tpm2-setup exited {rc}, expected 75"
            assert "dictionary attack lockout" in out, "no lockout notice"
            machine.fail(f"test -e {anchor}")
            assert machine.succeed(f"sha256sum {var_secret} /root/creds/nvpcr-anchor.test.cred") == stored, (
                "a stored anchor secret changed in lockout"
            )

    machine.succeed(f"rm -f {secret} && mv /root/anchor-secret.saved {secret}")

    with subtest("fixed tpm2-setup anchors the NvPCR once the lockout is cleared"):
        machine.succeed("tpm2_dictionarylockout --clear-lockout")
        assert not in_lockout(), "the lockout did not clear"
        rc, out = setup(fixed)
        print(out)
        assert rc == 0, f"fixed tpm2-setup failed after the lockout cleared ({rc})"
        machine.succeed(f"test -e {anchor}")
  '';
}
