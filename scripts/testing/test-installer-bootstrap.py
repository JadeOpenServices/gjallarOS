#!/usr/bin/env python3
"""Exercise source preparation using local Git repositories, never a host install."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


BOOTSTRAP = Path(__file__).resolve().parents[1] / "installation/install.sh"


class BootstrapSourceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="gjallar-bootstrap-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = dict(os.environ, GIT_CONFIG_GLOBAL="/dev/null",
                        GIT_CONFIG_NOSYSTEM="1", GIT_ALLOW_PROTOCOL="file")
        tools = self.root / "bin"
        tools.mkdir()
        nix = tools / "nix"
        nix.write_text('''#!/usr/bin/env bash
set -eu
test -f "$TEST_REPO/pkgs/monique/nix/nixos-module.nix"
touch "$TEST_REPO/nix-invoked"
for arg in "$@"; do
    case "$arg" in path:*)
        dir="${arg#path:}"; dir="${dir%%#*}"
        test "$dir" != "$TEST_REPO"
        test ! -e "$dir/.git"
        test -f "$dir/pkgs/monique/nix/nixos-module.nix"
        printf '%s\n' "$dir" >> "$TEST_REPO/nix-flake-dirs";;
    esac
done
case " $* " in *" --print-out-paths "*) printf '/unused-test-output\\n';; esac
''')
        nix.chmod(0o755)
        sudo = tools / "sudo"
        sudo.write_text("""#!/usr/bin/env bash
printf '%s\\n' "$@" >> "$TEST_REPO/sudo-invoked"
exit 97
""")
        sudo.chmod(0o755)
        self.env["PATH"] = str(tools) + os.pathsep + self.env["PATH"]
        # Never read the host's /etc/gjallar/repository: on an installed
        # gjallarOS it would send the bootstrap to the system checkout.
        self.repository_file = self.root / "repository"
        self.env["GJALLAR_SYSTEM_REPOSITORY_FILE"] = str(self.repository_file)
        # The curl path clones into GJALLAR_DIR or reuses it; keep it away
        # from any real checkout in $HOME.
        self.env["GJALLAR_DIR"] = str(self.root / "curl-checkout")
        self.env["GJALLAR_REPO_URL"] = str(self.root / "no-such-origin")

    def git(self, repo, *args):
        return subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test",
                               "-c", "user.email=test@example.invalid", *args],
                              env=self.env, check=True, capture_output=True)

    def install_script(self, repo):
        script = repo / "scripts/installation/install.sh"
        script.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(BOOTSTRAP, script)
        return script

    def repository_copy(self):
        # A copied repository without git data and without pkgs/monique.
        repo = self.root / "copy"
        self.install_script(repo)
        (repo / "flake.nix").write_text("{}\n")
        return repo

    def run_bootstrap(self, repo):
        # A new session has no controlling terminal, so /dev/tty cannot open.
        return subprocess.run(["bash", str(repo / "scripts/installation/install.sh")],
                              env=dict(self.env, TEST_REPO=str(repo)),
                              text=True, capture_output=True,
                              stdin=subprocess.DEVNULL, start_new_session=True)

    def test_plain_clone_initializes_pinned_submodule_before_nix(self):
        module = self.root / "module"
        module.mkdir()
        self.git(module, "init", "-q")
        (module / "nix").mkdir()
        (module / "nix/nixos-module.nix").write_text("{}\n")
        self.git(module, "add", ".")
        self.git(module, "commit", "-qm", "module")
        origin = self.root / "origin"
        origin.mkdir()
        self.git(origin, "init", "-q")
        self.install_script(origin)
        (origin / "flake.nix").write_text("{}\n")
        self.git(origin, "submodule", "add", str(module), "pkgs/monique")
        self.git(origin, "add", ".")
        self.git(origin, "commit", "-qm", "repository")
        clone = self.root / "clone"
        self.git(self.root, "clone", str(origin), str(clone))
        self.assertFalse((clone / "pkgs/monique/nix/nixos-module.nix").exists())
        result = self.run_bootstrap(clone)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((clone / "nix-invoked").exists())
        self.assertEqual(self.git(clone / "pkgs/monique", "rev-parse", "HEAD").stdout,
                         self.git(module, "rev-parse", "HEAD").stdout)
        # Nix saw only a staged copy without git history, removed afterwards.
        staged = set((clone / "nix-flake-dirs").read_text().split())
        self.assertTrue(staged)
        for directory in staged:
            self.assertFalse(Path(directory).exists(), directory)

    def test_incomplete_copy_fails_before_nix_or_host_changes(self):
        repo = self.repository_copy()
        result = self.run_bootstrap(repo)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("copy the complete repository", result.stderr)
        self.assertFalse((repo / "nix-invoked").exists())
        self.assertFalse((self.root / "curl-checkout").exists())

    def test_system_checkout_host_never_builds_another_checkout(self):
        # SYNTHETIC repository file naming the system checkout. Without a
        # terminal the redirect stops before sudo; with the checkout missing
        # it stops earlier. Either way nothing is built here.
        repo = self.repository_copy()
        for line in ("/etc/gjallar/system\n", "/etc/gjallar/system/\n"):
            self.repository_file.write_text(line)
            result = self.run_bootstrap(repo)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue("needs a terminal" in result.stderr
                            or "that checkout is missing" in result.stderr,
                            result.stdout + result.stderr)
            self.assertFalse((repo / "sudo-invoked").exists())
            self.assertFalse((repo / "nix-invoked").exists())

    def run_against_system_checkout(self, system_script, *args, channel=None):
        # SYNTHETIC system checkout: a private mount namespace puts a temp
        # directory over /etc/gjallar, so the real one is never read.
        if not Path("/etc/gjallar").is_dir() or shutil.which("unshare") is None:
            self.skipTest("needs /etc/gjallar as a mount point and unshare")
        etc = Path(tempfile.mkdtemp(prefix="etc-gjallar-", dir=self.root))
        script = etc / "system/scripts/installation/install.sh"
        script.parent.mkdir(parents=True)
        script.write_text(system_script)
        (etc / "repository").write_text("/etc/gjallar/system\n")
        repo = Path(tempfile.mkdtemp(prefix="copy-", dir=self.root))
        self.install_script(repo)
        (repo / "flake.nix").write_text("{}\n")
        env = dict(self.env, TEST_REPO=str(repo),
                   GJALLAR_SYSTEM_REPOSITORY_FILE="/etc/gjallar/repository")
        env.pop("GJALLAR_CHANNEL", None)
        if channel is not None:
            env["GJALLAR_CHANNEL"] = channel
        probe = subprocess.run(["unshare", "-rm", "true"], capture_output=True)
        if probe.returncode != 0:
            self.skipTest("user namespaces are not available")
        result = subprocess.run(
            ["unshare", "-rm", "bash", "-c",
             'mount --bind "$1" /etc/gjallar && shift && exec bash "$@"', "bash",
             str(etc), str(repo / "scripts/installation/install.sh"), *args],
            env=env, text=True, capture_output=True,
            stdin=subprocess.DEVNULL, start_new_session=True)
        self.assertFalse((repo / "sudo-invoked").exists())
        self.assertFalse((repo / "nix-invoked").exists())
        return result

    def test_channel_stops_at_system_checkout_without_channel(self):
        # Wiki 752 R38: an install.sh from before 9fdc7c26 has no channel text.
        old = "#!/usr/bin/env bash\nexit 0\n"
        for args, channel in ((("--channel", "staging"), None),
                              (("--channel=dev",), None),
                              ((), "stable")):
            result = self.run_against_system_checkout(old, *args, channel=channel)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("older than the --channel option", result.stderr)
            self.assertIn("gjallarctl rebuild --update", result.stderr)
            self.assertNotIn("needs a terminal", result.stderr)

    def test_system_checkout_with_channel_or_no_channel_goes_on(self):
        current = BOOTSTRAP.read_text()
        old = "#!/usr/bin/env bash\nexit 0\n"
        for system_script, args in ((current, ("--channel", "staging")),
                                    (old, ())):
            result = self.run_against_system_checkout(system_script, *args)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn("older than the --channel option", result.stderr)
            self.assertIn("needs a terminal", result.stderr)

    def test_other_repository_file_keeps_the_local_checkout(self):
        repo = self.repository_copy()
        self.repository_file.write_text("/etc/gjallar/system-other\n")
        result = self.run_bootstrap(repo)
        self.assertIn("copy the complete repository", result.stderr)
        self.assertFalse((repo / "sudo-invoked").exists())


if __name__ == "__main__":
    unittest.main()
