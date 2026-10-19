# GjallarOS roadmap: USB trust, passkeys, device tooling

Status as of 2026-09-30. `[x]` = done and committed; `[ ]` = open.
Research notes are from memory (knowledge up to mid-2026). Before acting on a
vendor claim, check the linked project's own docs.

## 1. USB trust enforcement (arm / disarm)

Goal: USBGuard blocks unknown devices. Internal ODDC devices are enrolled once,
and only the disk encryption passphrase can turn blocking off again.

- [x] The signed trust document carries the `enforcement` state. Legacy
      signatures stay valid.
- [x] The first arming auto-enrolls internal ODDC devices. Re-arming enrolls
      nothing.
- [x] `disarm` is verified against every `boot.initrd.luks.devices` entry via
      `cryptsetup --test-passphrase`, with a 3 s delay on failure.
- [x] `gjallarctl usb disarm`: no-echo prompt on a terminal, or piped stdin.
      Run it as `sudo gjallarctl usb disarm`.
- [x] The runtime `ImplicitPolicyTarget` follows the armed state. Audit mode
      leaves USBGuard's configured fallback alone.
- [x] The `gjallar-usbtrustd` and `gjallarctl` packages build in the Nix
      sandbox, with tests.
- [x] The installer sets the TPM handle and offers blocking from first boot
      (encrypted root and TPM2 only, default no). The broker creates or
      adopts the signing key on first start. VM-tested arm, block, disarm and
      re-arm on fw13 (2026-10-10).
- [ ] **Rollout** (on the live host, run by the user):
  1. `rebuild` with `usbTrustEnforce = false`. Check that
     `gjallarctl usb status` reports `"armed": false` and that the review
     notices still work.
  2. Test the password fallback: click "Use password" during the scan. The
     askpass dialog must appear at once.
  3. Set `"usbTrustEnforce": true` in `user.config.json` (untracked), then
     `rebuild`.
  4. Check that `gjallarctl usb status` reports `"armed": true`. The fingerprint
     reader and Bluetooth should be enrolled. The passkey and USB disk should
     wait for review.
  5. Break-glass test: `sudo gjallarctl usb disarm`. A wrong passphrase must
     fail after about 3 s; the right one must disarm.
  6. Turning enforcement off for good: set `"usbTrustEnforce": false`,
     `rebuild`, then `sudo gjallarctl usb disarm`. Disarm alone does not
     last: the broker re-arms from the config on its next restart.
- [ ] Show armed or audit mode in the review dialog header, not only in
      `status` JSON.
- [ ] Remaining caveats, which need real fixes, not just docs:
  - **Rollback.** Root can restore an older signed document from a
    pre-arming state. Fix: a TPM NV monotonic counter bound to `Revision`.
  - **Root tamper.** Root can stop the broker or USBGuard. That is out of
    scope for a single-user machine, but audit-log it.
  - **Lockout.** If the internal keyboard were USB and not enrolled, the
    passphrase would have to be typed on it. Keep the laptop keyboard
    (i8042/i2c, not USB) in mind, and test in a VM before flipping on new
    hardware.

## 2. USB review UX

- [x] Plain verdict with colour dots, and an optional technical view.
- [x] Fingerprint first, with a "Use password" button.
- [x] **Fixed:** "Use password" did nothing. sudo ignores SIGTERM while
      pam_fprintd waits, so the password phase ran only after a finger touch,
      and then silently on the fresh timestamp. The scan is now SIGKILLed and
      the timestamp dropped before the askpass prompt.
- [x] **Unplug closes prompts.** Each decision polls policy every second. When
      the connection disappears, the notice (SIGINT), review dialog,
      fingerprint scan and askpass group (SIGKILL on the process group) all
      close, and no request is sent. The broker already rejects stale
      connection tokens (`sessions.Check`), so a stale entry can never
      authorize.
- [ ] React to events instead of polling: subscribe to the USBGuard IPC or
      D-Bus `DevicePresenceChanged`, and close within ~100 ms instead of ~1 s.
- [ ] Composite-device warning: flag devices whose interfaces mix HID with
      storage or network (BadUSB pattern). Suggest "keep blocked" by default.
- [ ] Time-limited allow ("for 1 hour") next to "until unplugged".
- [ ] Audit trail: one journal line per decision (who, device, action, auth
      method), readable via `gjallarctl usb log`.

## 3. Device tooling

- [x] `system/tools/android.nix`: `android-tools` (adb, fastboot). NixOS 26.05
      removed `programs.adb`; systemd 258 uaccess rules replace the
      `adbusers` group. USBGuard must still allow the phone. Note that "Allow
      until unplugged" re-prompts when the phone switches USB mode (MTP ↔ adb
      changes the interface set).
- [x] `system/tools/fido2.nix`: `libfido2` (`fido2-token -L`, `-I`, PIN,
      reset) and `fido2-manage` (list and delete resident passkeys).
      systemd's `60-fido-id.rules` grants hidraw access. No PAM login is
      wired to the key.
- [ ] Optional, vendor-specific: `yubikey-manager` for a YubiKey,
      `solo2-cli` for a SoloKey 2, `nitrokey-app2` for a Nitrokey. Add only
      the one you own.
- [ ] Optional: `pam_u2f` as a second factor for sudo or hyprlock. That is a
      separate decision, because it changes the login stack.

## 4. Browser and passkeys

### Diagnose first: LibreWolf already supports hardware passkeys

Firefox, and so LibreWolf, uses `authenticator-rs` for CTAP2 on Linux. That
covers PIN, discoverable (resident) credentials and passkeys on a USB security
key. No Linux browser has a built-in synced "platform" passkey store; those
live on the key or in a password-manager extension. If passkeys fail today,
the likely causes, in order:

1. USBGuard blocks the key (`1ea8:f825` is currently `unknown-external`). Use
   "Always allow → portable".
2. No hidraw access. `fido2-token -L` must list the key without sudo.
3. The site offers only "phone / hybrid" passkeys. Firefox on Linux has
   limited hybrid (QR plus Bluetooth) support. Chromium's hybrid uses a
   Google-run relay.
4. Resident-key slots are full. Check with `fido2-manage`.

Test at https://webauthn.io, trying both the "discoverable" and "security key"
options.

### Synced passkeys without a cloud vendor

- **KeePassXC plus KeePassXC-Browser** (recommended). It is open source,
  local-only, stores passkeys (2.7.7+), and has no telemetry. The `.kdbx` file
  can sync through the existing Nextcloud. It works in LibreWolf.
- Bitwarden or Vaultwarden: open source, audited yearly (Cure53 and others),
  and passkeys work in the Firefox extension. Self-host Vaultwarden to avoid
  the vendor cloud.
- The Nextcloud Passwords extension in use today: as far as known, it has no
  passkey support. Keep it for passwords, or migrate.

### Browser options, all open source

| Browser | Base | Telemetry / phone-home | Community scrutiny | Passkeys (HW key) | Verdict |
| --- | --- | --- | --- | --- | --- |
| **LibreWolf** (current) | Firefox stable | Removed at build; no studies, no Pocket | Mature, Codeberg, widely reviewed | Yes | **Keep**; fix the USB and hidraw path |
| Firefox + policies | upstream | Opt-out; disable through `programs.firefox.policies` (`DisableTelemetry`, `DisableFirefoxStudies`, …) | Highest, Mozilla security team | Yes | Fallback if LibreWolf lags on patches |
| Mullvad Browser | Firefox ESR (Tor Project) | None; independently audited (Cure53) | High | Yes, but private mode always on | Privacy tool; bad for staying logged in |
| Zen | Firefox | Disabled by default; young project | Growing, smaller | Yes | Removed 2026-10-09; not needed next to Firefox |
| Floorp | Firefox ESR | Disabled | Small | Yes | No clear gain over LibreWolf |
| Waterfox | Firefox ESR | Minimal; search partnership | Moderate | Yes | No clear gain |
| ungoogled-chromium | Chromium | Google services stripped | High | Yes (HW key); hybrid questionable | **Used** for the web apps and "Register key" (Chromium sets a missing key PIN) |
| Brave | Chromium | P3A, rewards, wallet, update pings (opt-out) | High, but vendor-driven | Yes | **Excluded**: phones home by default |
| Vivaldi | Chromium | — | — | — | **Excluded**: UI is closed source |

Next steps:

- [ ] Allow the passkey in USB Guard, then run `fido2-token -L` and the
      webauthn.io test.
- [ ] Decide on KeePassXC (local plus Nextcloud sync) or Vaultwarden for
      synced passkeys.
- [ ] Optionally pin the LibreWolf policies (disable the built-in password
      manager if KeePassXC takes over).

## 5. How others handle USB device control

| Platform | Mechanism | Unknown device | User approval flow | Unplug / stale approval | Break-glass |
| --- | --- | --- | --- | --- | --- |
| **Windows** (GPO / Intune) | Device Installation Restrictions: allow and deny lists by hardware ID, instance ID, setup class; "prevent installation of devices not described" | Driver install blocked; toast "blocked by policy" | None built in; IT changes policy (ticket) | Rules match IDs, not a session; re-plug is re-evaluated | Admin changes policy; local admin can override unless locked |
| **Microsoft Defender for Endpoint** Device Control | Reusable groups (VID/PID, serial, instance path, bus); per-user and per-group read/write/execute masks; **audit and enforce modes** | Deny or audit, with an event in the Defender portal | None native; audit first, then allow-list | Per connection; events show insert and remove | Policy change from the portal |
| **NinjaOne** (RMM) | No deep native USB engine; uses Windows policies and scripts, or the device control of the bundled AV (e.g. Bitdefender GravityZone) | Depends on the AV module | Technician changes policy | AV-dependent | Technician or remote script |
| **Ubuntu** (Landscape, Ubuntu Pro USG, Intune-for-Linux) | USG (CIS/STIG) disables `usb-storage`; USBGuard is available but not managed by Landscape; Intune on Ubuntu checks compliance only | Storage blocked by kernel module denylist; others allowed | None | n/a | Root or remote script |
| **RHEL** | **USBGuard** in base since 7.4; rule language (`hash`, `serial`, `via-port`, `with-interface`); `usbguard-notifier` desktop notices; STIG requires it; rules distributed with Ansible or Satellite | `ImplicitPolicyTarget=block` | `usbguard-notifier` informs; allowing needs root or IPC ACL | Device IDs are per connection; a removed device's allow is gone | Root edits rules |
| **GjallarOS** | USBGuard plus a TPM-signed trust store plus ODDC hardware model | Blocked (armed) or audit | Notice → review → fingerprint or password | Connection token; broker rejects stale; prompts close on unplug | Disk passphrase (`usb disarm`) |

Ideas worth taking:

- [ ] **Audit mode before enforce** (Defender). Already the default, but add an
      "audit summary" of what *would* have been blocked in the last N days
      before arming.
- [ ] **Interface-aware rules** (USBGuard `with-interface`). Store the
      interface set in the trust record, and re-prompt if a trusted device
      suddenly adds HID.
- [ ] **Access masks** (Defender): allow storage read-only as a third option
      next to allow and block. This needs udev or a mount policy, not only
      USBGuard.
- [ ] **Offline temporary unlock code** (the Endpoint Protector-style
      break-glass) as an alternative to typing the disk passphrase.
      Low priority.
- [ ] **Central export**: `gjallarctl usb export` of the signed policy for
      JODS-managed machines, and an Ansible-style apply for fleets.

## 6. Paused (until explicitly resumed)

- Installer E2E: TPM2 target check, e2e-full, audits, docs.
- Nextcloud effective-pin dehydration verification.
- microSD reader reports zero capacity.
- JODS clean-slate reset for unmanaged installs.

## 7. Displays and Monique

- [ ] **Display scaling from the Noctalia control center.** On the HP ZBook
      x2's high-resolution screen everything is small enough that touch is
      hard to use. Add a scale control to the Noctalia control center that
      sets the scale through Monique, so users can change it as soon as they
      notice. No scale is forced by default; the user picks it.
- [ ] **Monique starts faster.** It still takes about a second too long to
      open. Measure where the time goes before changing anything.

## 8. Secure Boot on devices without an ODDC setup

- [ ] **Best-effort Secure Boot where ODDC has no setup.** Covers unknown
      machines with a draft profile and known models without a Secure Boot
      setup, like the HP ZBook x2 G4. The installer asks whether the user
      wants to try the generic setup anyway: own keys through setup mode,
      Microsoft keys kept. The default is no. Before anything is written,
      warn and ask for typed confirmation that this can fail in ways nobody
      has tested on this model, and that recovering may need clearing the
      Secure Boot keys or resetting them to factory defaults by hand in
      firmware setup.
      - When the firmware already enforces Secure Boot, this question comes
        first. Only when the user says no does the installer warn and offer
        to reboot into firmware setup or continue (done in the installer
        today, without the question).
      - The result goes into the draft profile. Contributing it is optional
        and separate from the rest: a user can share the other hardware facts
        without the Secure Boot result, so ODDC still grows from many
        installs, and someone can turn a working attempt into a real setup
        later.
- [ ] **Desktop notice for Secure Boot state.** Keys enrolled but firmware
      enforcement still off shows only in the journal today. Show it like the
      low-battery warning.

## 9. Accounts

- [ ] **Separate daily and admin accounts.** Today the installer creates one
      account that is in `wheel` and does everything. Malware running as that
      user can wait for a sudo or `rebuild` password and reuse it. Option at
      install time: a daily account without admin rights, and a second admin
      account that only `gjallarctl` operations and polkit ask for. The daily
      user then types the admin password, not their own, so a keylogger in the
      daily session still has to catch the separate password. Default stays a
      single account until the flow is tested on real hardware.

## 10. ODDC beyond GjallarOS

- [ ] **Screen rotation from ODDC on any NixOS install.** Today the GjallarOS
      installer reads `hardware.sensors.hub.provides.orientation` from the
      ODDC model and sets `orientationSensorEnable`, and GjallarOS's Hyprland
      unit does the rotating. ODDC is meant to work without GjallarOS, so the
      part that does not depend on the compositor could live in ODDC's own
      NixOS module: turn on `hardware.sensor.iio` (iio-sensor-proxy) when the
      model has the sensor. GjallarOS would then keep only the Hyprland side
      (`gjallarctl hyprland-rotate`). ODDC agreed and built it as ODDC-95
      (`oddc.sensors.orientation.enable`, sets `hardware.sensor.iio.enable`
      with `mkDefault`). When the GjallarOS pin reaches it: `input.nix` sets
      iio only through `mkIf` on the old installer flag, the rotation unit
      turns on from `config.hardware.sensor.iio.enable`, and the installer
      stops writing `orientationSensorEnable`. Open question to ODDC: can a
      light-sensor-only model get iio, so rotation needs its own flag?

## 11. Releases and channels

Plan agreed 2026-10-10. Nothing in this section is built yet.

- [ ] **Three channels.** `dev` is `main`. `staging` and `stable` are
      branches that only ever fast-forward to a signed, annotated tag
      `vYYYY.MM.N` (N counts the releases in that month). A tag never moves;
      a fix found in staging gets a new tag.
- [ ] **Release flow.**
      1. A branch bumps all flake inputs (nixpkgs, Home Manager, Noctalia,
         the ODDC pin and the rest).
      2. The full `.vm/installer-e2e/REGRESSION.md` runs in VMs.
      3. Merge to `main` and tag.
      4. `staging` moves to the tag. The Framework 13 and the ZBook x2 G4
         run it for a while.
      5. `stable` moves to the same tag.
- [ ] **Picking a channel.** `user.config.json` gets `releaseChannel`
      (`dev`, `staging` or `stable`). `rebuild --update` fetches that branch
      and only fast-forwards from the commit that is checked out, after the
      signature check below. The bar shows a notice when the channel is
      ahead. `deployment/release-policy.json` (today `updateEndpoint: null`,
      `updatePolicy: review-and-lock`) is where the channel defaults live.
      Open: the default channel for new installs, and what existing installs
      on `main` move to.
- [ ] **ODDC stays separate.** A release carries whatever ODDC commit its
      `flake.lock` pins; the next item keeps that pin current. `rebuild --hardware-update --channel stable|staging`
      keeps working as it does today and is not tied to the GjallarOS
      channel.
- [ ] **ODDC pin follows ODDC stable** (Jade, ODDC items 83, 100). Security
      findings and catalog fixes must reach machines without waiting for a
      GjallarOS release. ODDC promises that every commit on its `stable` is a
      release and is never force-pushed. A workflow moves the pin; people
      still decide what lands.
      1. **Trigger.** Hourly schedule plus manual start. It polls ODDC
         `stable` with `git ls-remote` and stops when the pin is current. No
         webhook: that would need a token from ODDC that can write here.
      2. **No secrets.** The job has no access to the `release-cache`
         environment and never signs anything. Its token may only push the
         branch `ci/oddc-pin` and open or update one pull request.
      3. **Forward only.** The new commit must descend from the pinned one
         (`git merge-base --is-ancestor`), so no rollback and no side
         branch. `nix flake update oddc` must change only the `oddc` node in
         `flake.lock`, and nothing else in the tree.
      4. **Checks**, all at the old and the new commit, for each model we
         ship or test (`fw13`, ZBook x2 G4, HP, book12):
         - `oddc validate --root DIR --json` passes.
         - `oddc status --root DIR --json`: a model that was verified and is
           no longer verified fails the job, and the pull request says
           which one.
         - `oddc security --json`: a flag GjallarOS reads that existed at the
           old commit and is gone at the new one fails the job (job 29.1).
           The flags GjallarOS reads are listed in one file next to the
           code that reads them, with no per-model values.
         - `prebuilt` keeps its drvPath. An ODDC bump then never makes slow
           devices compile, so it needs no release cache build (item 80).
           If the drvPath changes, the job fails and says so.
         - Every `nixosConfigurations` entry still evaluates.
      5. **The pull request** says what the bump brings from
         `oddc changes --from OLD --to NEW --json` (data, evidence, schema,
         nix, go, docs) and lists new or changed security findings by id.
         A failed check leaves the pull request open with the failure in
         it, so nobody misses it.
      6. **Landing.** A maintainer merges it like any other change. Once
         commit signatures (below) exist, they land it with their own signed
         commit, because a key the workflow holds must never be able to
         update machines.
      7. **Reaching machines.** `dev` gets it with the next
         `rebuild --update`. For `staging` and `stable`, a bump whose
         `oddc changes` classes are only data, evidence or docs may get its
         own tag on top of the current one without the soak, because flags
         only lower trust. A bump that changes ODDC's nix or Go code waits
         for the normal release. The tag is still signed by a person. The
         notice from job 39 tells the user about a new finding after the
         rebuild.
      Open for Jade: poll interval, who may land these pull requests, and
      whether the data-only fast lane to `stable` is wanted.
- [ ] **Verifying updates.** Goes through the research flow again before
      any code. Ideas so far, from the sources below:
      - The trust root comes with the install, not from the server: the
        first commit the rule applies to and the key that signed it, like a
        Guix channel introduction.
      - The repo lists the keys allowed to sign, like
        `.guix-authorizations`. A commit counts only when it is signed by a
        key listed in its parent, so adding or removing a key is itself a
        signed commit.
      - No rollback: an update that is not a fast-forward from the deployed
        commit stops, unless the user asks for a downgrade on purpose. Guix
        `guix pull` does the same.
      - One stolen key must not be enough to push an update to every
        machine. Options: an offline master key that signs release and
        developer keys and can revoke them (Qubes), developer keys that
        expire after a year (Qubes), and two signatures per release tag
        (TUF thresholds).
      - A mirror or a stale fork can serve an old `stable` forever (TUF
        "indefinite freeze"). The release tag carries its date; `rebuild`
        warns when the newest tag on the channel is very old.
      - Check signatures locally. GitHub's "Verified" badge is not trusted
        (Qubes guide).
- [ ] **GitHub releases.** A tag and release notes built from the `GJAL`
      commit subjects since the previous tag. No ISO and no other binaries:
      everyone builds the ISO on their own machine from the tag, so the
      source and the build can be inspected. README gets the steps to build
      the ISO from a release. Later, once the ISO build is reproducible,
      the notes can list the expected ISO hash so builders can compare.
- [ ] **Binary cache for our own packages.** Patched Noctalia,
      noctalia-greeter, gjallarctl, monique and the other packages in
      `pkgs/` are not on cache.nixos.org. Upstream caches do not match
      either, because the inputs follow our nixpkgs and carry our patches,
      so slow devices compile them for a long time. Built: GitHub runners
      build the release tags and add them, signed, to the `nix-cache`
      release (README "Release cache"); those files are the only binaries
      in a release. The installer and `rebuild` add it as a substituter.
      Devices still evaluate the same source; the cache only skips building
      outputs with a matching hash, and `nix build --rebuild` can check it.
      Research flow first, because the cache signing key becomes a key every
      machine trusts: build only signed tags, keep the key in a protected
      GitHub environment. Still open: let the installer ask whether to use
      the cache, and a second builder that rebuilds and compares.
      Measured 2026-10-11 (fresh store, only cache.nixos.org trusted, 16
      threads): 46 min wall, 4.9 h summed build time. Our KIO capacity
      patch takes 37 % (KIO plus the 26 KDE packages that link it: Dolphin,
      Ark, Konsole, Baloo, Darkly and others), Noctalia with its libqalculate
      patch 13 %, the Nextcloud client with OpenVFS 8 %, opencode 4 % (not
      on cache.nixos.org for our nixpkgs pin), the qt6ct patch 2 %. A cache
      of these saves about 63 %. Kernel modules and the initrd depend on the
      device and always build locally (2 %).

Sources:

- Guix channel authentication:
  https://guix.gnu.org/manual/en/html_node/Channel-Authentication.html
- Guix, "Securing updates" (2020):
  https://guix.gnu.org/en/blog/2020/securing-updates/
- Qubes, verifying signatures:
  https://doc.qubes-os.org/en/latest/project-security/verifying-signatures.html
- The Update Framework, attacks and weaknesses:
  https://theupdateframework.io/docs/security/

## 12. Secure Boot on installed systems

Plan written 2026-10-12. Today only the installer sets up Secure Boot. An
installed system without it (the ZBook x2 G4, installed before its ODDC
setup existed) can only get it by running the whole installer again.

- [x] **`gjallarctl secure-boot setup`.** Does what the installer does,
      in the same order, from the installed system:
      1. Resolve the device and its ODDC Secure Boot setup the way the
         installer does. No setup for this model: stop and point to §8.
      2. Read the firmware state. Factory keys with Secure Boot off is the
         start. A pending or finished GjallarOS enrollment resumes where it
         stopped. Setup Mode without our record, someone else's keys, or a
         mixed state: stop and say what to do (for HP: restore the factory
         keys in firmware setup, then run the command again).
      3. Show the plan, create the keys, show the recovery archive and
         passphrase, ask twice that they are stored.
      4. Set `secureBootEnable` in `user.config.json` and rebuild, so the
         next boot loads signed images through lanzaboote.
      5. `sbctl verify`, save the firmware setup, arm the enrollment, show
         the firmware steps and reboot into firmware setup.
      The boot services that exist for the installer then enroll the keys,
      ask to turn on Secure Boot and verify it. The installer and this
      command call the same code for steps 2, 3 and 5; nothing is copied.
      zsh completion like every other command. gjallarctl asks for sudo
      itself.
- [x] **Restore dbx before enrolling.** HP's "Clear Secure Boot Keys" also
      empties dbx, the list of revoked boot files. Without it, Secure Boot
      starts boot loaders that are known to be exploitable. Rule: in Setup
      Mode, when the ODDC setup wants dbx present, dbx is empty and the
      firmware has a default dbx (dbxDefault), write dbxDefault to dbx,
      signed with our KEK, read it back and compare, then enroll. Any
      mismatch stops before the PK is written. sbctl cannot write dbx since
      0.13, so this uses efitools (`sign-efi-sig-list`, `efi-updatevar`),
      as the Arch wiki describes. Open: ask ODDC for a structured field
      instead of matching on the instruction text.
- [x] **Microsoft KEK 2023.** Microsoft's KEK CA 2011 expired in June
      2026. New dbx updates are signed with KEK 2K CA 2023. A KEK update
      has to be signed by the PK, and after enrollment the PK is ours, so
      neither Microsoft nor HP can add it later. When the ODDC setup keeps
      the firmware KEKs and KEKDefault has the 2011 CA but not the 2023 one,
      enroll KEK 2K CA 2023 as well, through sbctl's
      custom KEK folder (`-c`). The certificate is pinned by hash. db stays
      as the firmware had it (no `-m`), so nothing new is trusted to boot.
      Open: ask ODDC whether the ZBook's KEKDefault already has it.
- [x] **dbx updates after enrollment.** The fwupd refresh timer keeps the
      LVFS data current, but a dbx update changes PCR 7, so it never installs
      on its own. After Secure Boot is verified, setup
      offers `gjallarctl secure-boot update-dbx`, a dbx update through fwupd (2.0.8 or newer handles
      the 2023 KEK). dbx is measured into PCR 7, which TPM disk unlock is
      bound to, so before the reboot the Secure Boot policy is locked again
      and the TPM policy predicted for the new dbx. Otherwise the next boot
      asks for the recovery key. The PCR 7 state of the running boot stays
      valid until the restart, so a rebuild in between still matches.
- [x] **Running boot stays in the TPM policy.** lzbt deletes the pcrlock
      files of boot files it no longer installs. With one generation
      (recovery installs) every rebuild deleted the running UKI's, and the
      policy dropped PCR 4 until the next boot: an older UKI signed with the
      same key could unlock the disk in that window. The install hook now
      keeps what this boot measured as one more variant; boot removes it
      before the policy is made (e2e-fw13, 2026-10-12).
- [ ] **Kernel lockdown (job 25.5, decision needed).** With Secure Boot on,
      root can still reach the firmware and the running kernel: /dev/mem,
      MSR writes, unsigned kexec, unsigned kernel modules. Fedora and Arch
      turn on lockdown with Secure Boot for this. The NixOS kernel we use
      (`linuxPackages_latest`) is built without the lockdown LSM and without
      module signing, so `lockdown=integrity` alone does nothing. It needs:
      - our own kernel config (lockdown LSM, module signing, enforced
        signatures), so the kernel and its modules always build locally,
        never from cache.nixos.org;
      - a module signing key: a throwaway key per build as Fedora and Arch
        do, plus a kept key for out-of-tree modules like the NVIDIA driver;
      - giving up hibernation, which lockdown blocks;
      - leaving it off in the firmware flash read entry
        (`firmwareFlashReadEntry`), which reads the chip through `/dev/mem`.
      Jade decides whether that trade is worth it before anything is built.

Sources:

- sbctl manual (enroll-keys, custom KEK and db):
  https://man.archlinux.org/man/sbctl.8.raw
- sbctl 0.13 dropped dbx enrollment:
  https://src.opensuse.org/utilities/sbctl/src/branch/main/sbctl.changes
- Lanzaboote quick start:
  https://github.com/nix-community/lanzaboote/blob/999c0cb03f748fe311bca78961dbf0562dc91659/docs/QUICK_START.md
- Arch wiki, Secure Boot (efitools, dbx):
  https://wiki.archlinux.org/title/Unified_Extensible_Firmware_Interface/Secure_Boot
- Microsoft certificates expiring in 2026:
  https://eclypsium.com/blog/microsoft-secure-boot-certificates-expire-2026/
- AlmaLinux, 2023 certificates:
  https://wiki.almalinux.org/documentation/secure-boot-2023-certificates.html
- Rocky Linux, CA and KEK transition:
  https://rockylinux.org/news/2026-06-02-secure-boot-ca-kek-transition
- Google Cloud, Microsoft Secure Boot certificate update:
  https://docs.cloud.google.com/compute/docs/security/ms-secure-boot-certificates-update
- fwupd, db and KEK updates:
  https://github.com/fwupd/fwupd/issues/9191
- Lockdown on NixOS:
  https://git.afnix.fr/raito/lanzaboote/issues/88
  https://discourse.nixos.org/t/how-to-enable-secureboot/28820/10
- Lockdown and hibernation:
  https://lkml.iu.edu/hypermail/linux/kernel/1908.2/02835.html

## 13. JODS: own pins and build agents

Plane GJALLAROS-183, weight 44. Nothing in this section is built yet.

Jade's idea (2026-10-13): GjallarOS pins its packages in Nix. JODS should
also be able to pin its own things, such as Noctalia plugins or a company's
own Firefox add-ons. An organisation runs its own build agents, which build
these for the GjallarOS channel the devices are on. Devices then only
download them.

### Is it sane?

Mostly yes, because Nix already has the parts:

- A pin is a flake input or a fetcher with a hash, in a repository JODS
  owns. Devices evaluate it like the GjallarOS repo itself.
- A build agent builds the pinned sources and puts the outputs in a binary
  cache. Devices list that cache as a substituter and get the outputs
  instead of compiling them, like the release cache in section 11.
- The pins are hashes, so devices need no credentials for a private
  repository. Only the agent fetches sources.

Three parts need more than that:

- **Build per channel.** Noctalia plugins follow the Noctalia plugin API of
  the release. The agent builds every pin once per GjallarOS channel
  (`dev`, `staging`, `stable`) against that channel's `flake.lock`. A pin
  that does not build on a channel is not offered there.
- **Firefox add-ons stay signed by Mozilla.** Release Firefox only installs
  add-ons Mozilla has signed. A company's own add-on gets signed as an
  unlisted add-on on addons.mozilla.org
  ([Extension Workshop](https://extensionworkshop.com/documentation/publish/signing-and-distribution-overview/)),
  and the pin is that signed file with its hash. Turning signature checks
  off is not an option: it would let every unsigned add-on in.
- **Noctalia plugins are plain code in the session.** They run with the
  user's rights and nobody signs them. A plugin pin is trusted like a
  package in the system: someone reviews the source at the pinned commit.

Standalone installs do not need any of this. They keep the GjallarOS pins
and can add their own overlay in their own checkout.

### Security pass

- **What the cache key can do.** A device checks a downloaded path against
  the keys in `trusted-public-keys`
  ([Nix manual](https://nix.dev/manual/nix/latest/command-ref/conf-file.html#conf-trusted-public-keys)).
  A trusted key is valid for every store path, not only for the
  organisation's plugins. Whoever holds it can serve their own content for
  any path a device asks that cache for, system packages included.
- **Build servers get attacked directly.** In the SolarWinds case an
  implant on the build server swapped a source file during the build, so
  the shipped and signed product carried the backdoor
  ([CrowdStrike, SUNSPOT](https://www.crowdstrike.com/blog/sunspot-malware-technical-analysis/)).
  A signature only proves where the output came from. It does not prove
  that the build was clean.
- **Shared build state gets poisoned.** GitHub Actions cache poisoning
  lets a low-trust job leave entries that a release job later uses
  ([Adnan Khan, 2024](https://adnanthekhan.com/2024/05/06/the-monsters-in-your-build-cache-github-actions-cache-poisoning/)).
  Nix itself had a sandbox hole where a fixed-output build could change a
  store path after Nix had marked it valid
  ([CVE-2024-27297](https://nvd.nist.gov/vuln/detail/cve-2024-27297)).
- **Pins delay updates, they do not review them.** In December 2024 a
  phished developer account pushed a malicious update of the Cyberhaven
  browser extension, and auto-update delivered it to about 400,000 browsers
  within hours ([Island](https://www.island.io/info/cyberhaven-and-extension-takeover-incident),
  [DomainTools](https://dti.domaintools.com/cyberhaven-breach-likely-part-of-a-long-term-criminal-campaign/)).
  Firefox also grants a policy-installed add-on the new permissions of an
  update without asking (`isAddonRequiredByPolicy` in Firefox 154). A bot
  that bumps pins to the newest version gives the same result as
  auto-update.

Threat model: an agent is a server on the organisation's network and can
be taken over. Its disk and its secrets count as readable. A device in a
user's hands must not trust more than the organisation decided to ship.

### Decision

- [ ] **The signing key never sits on a build agent.** The agent builds and
      uploads unsigned. A separate JODS signer signs only store paths whose
      output hash two agents on different machines both got (Nix
      `--rebuild` checks). The release key is never the cache key, so one
      stolen key is never enough (as in section 11).
- [ ] **Devices accept only what a signed manifest lists.** JODS publishes a
      manifest per channel: pin, version, store path, output hash, and for
      add-ons the permissions. Devices verify it with the JODS key from
      enrolment and only fetch those paths from the organisation's cache.
- [ ] **Agents keep no state between jobs.** No shared cache between
      review builds and release builds; Nix kept at a patched version; one
      agent pool per organisation.
- [ ] **Pin bumps are reviewed.** A bump shows the source diff (plugins) or
      the permission diff (add-ons), and a person approves it in JODS. A
      bump that adds permissions always needs that approval, never a bot.
- [ ] **Ties to managed items.** The pins become items in the same
      `app, id, source, pin, mode` model as the Firefox add-ons (Plane
      GJALLAROS-180) and the other app backends (GJALLAROS-182), so the app
      portal shows them like any other item.

The idea holds. What does not hold is any version where the build agent
signs on its own, or where pins follow upstream automatically.

## 14. System checkout owned by root

Plan written 2026-10-18 for Plane GJALLAROS-168 (loophole 20, also the root
of loophole 22). Today root builds the system from `~/Documents/gjallarOS`.
The desktop user owns that checkout, including `generated/`,
`user.config.json` and `user/`, which home-manager evaluates as part of the
system. `DiscoverRepository` also asks `GJALLAROS_REPO` and a state file in
the user's home before `/etc/gjallar/repository`. So anything running as
the user decides what root builds the next time someone types the password
for `rebuild`. On a managed device, where the user has no root, that is a
way to root.

### How others handle it

- NixOS users raised the same question in
  [Security: auto-upgrades and config ownership](https://discourse.nixos.org/t/security-auto-upgrades-and-config-ownership/30970).
  Most answers keep the configuration that root builds under root: a
  root-owned worktree reset to a branch by a root alias, a copy inside the
  system closure (`/run/current-system/configuration`), or a flake on a
  locked-down remote. Two replies argue that on a single-user desktop the
  user account is worth as much as root anyway.
- Fedora's image-based desktops (Silverblue, Kinoite) keep the system tree
  read-only and change it only through `rpm-ostreed` behind polkit. The
  [OSTree SIG proposal](https://gitlab.com/fedora/ostree/sig/-/issues/7)
  lets unprivileged users update but asks for a password for any change to
  the system, and notes that files like `~/.bashrc` and `$PATH` stay a
  separate trust problem.
- `/etc/nixos` is root-owned by default on NixOS; `nixos-rebuild` with a
  user-owned flake is a choice the user makes.
- sudo resets the environment by default because
  [a blocklist of dangerous variables cannot be complete](https://www.sudo.ws/security/advisories/env_add/).
  `GJALLAROS_REPO` deciding what root builds is the same kind of input.

### Security pass

- **What the current design allows.** The user side picks the checkout
  (environment variable, state file, working directory), pulls, rewrites
  `user.config.json` and evaluates. The root side activates what that
  produced. Root never checks where the source came from.
- **How this broke elsewhere.** git's
  [CVE-2022-24765](https://github.blog/2022-04-12-git-security-vulnerability-announced/)
  ran another user's commands because git read configuration from a
  repository someone else controlled; the fix was the `safe.directory`
  ownership check. [CVE-2024-32004](https://access.redhat.com/security/cve/CVE-2024-32004)
  did the same through a crafted local repository at clone time. So root
  must never run git inside, or clone from, the user's checkout.
  [CVE-2023-22809](https://security-tracker.debian.org/tracker/CVE-2023-22809)
  (sudoedit) shows the wider pattern: a privileged tool trusted a value the
  user set and touched files nobody approved.
- **Threat model.** Two cases. Managed device: the user is not in wheel
  and root belongs to the organisation; today the user (or malware in the
  session) can make the next admin rebuild run their code. That is a real
  boundary and this plan closes it. Unmanaged device: the user is in wheel.
  Malware in the session can also wrap `sudo` through the shell startup
  files or `$PATH` and catch the password; this plan does not fix that
  (residual, logged on the security review page). What it does give there:
  the system builds only what was shown and approved, the built revision is
  recorded, and a checkout that is empty or not a git checkout (ODDC b12
  items 2 and 17) is reported instead of failing silently.
- **What changes in the design.** Root never reads the user's checkout
  with git and never follows a path the user chose. Settings arrive as
  typed values through a root service; local code arrives as a copied
  folder (data only) in developer mode. Discovery for privileged commands
  is fixed at `/etc/gjallar/system`.

### Plan

- [x] **Checkpoint 1: root-owned checkout.** `/etc/gjallar/system`,
      owned by root, not writable by group or others, a git checkout of the
      official origin. `generated/` and `user.config.json` live there too.
      `user.config.json` holds no secrets and stays readable for the desktop
      tools; its values reach the world-readable Nix store with every
      rebuild anyway, so secrets never go into it. `WriteAtomic` keeps an
      existing file's mode (minus group and other write); a new one is 600.
      `/etc/gjallar/repository` names it once `dotfilesDir` is
      `/etc/gjallar/system`, and `programs.nh.flake` follows. Before anything
      in it is evaluated, `gjallarctl system check` walks the checkout
      without following links: every entry and every directory above it is
      owned by root and not writable by group or others, no link leaves the
      checkout, and an empty or missing checkout is named as such.
- [x] **Checkpoint 2: privileged rebuild ignores user input.** On a system
      checkout the user's `rebuild` only checks its options, authenticates
      and starts `/run/current-system/sw/bin/gjallar-system-rebuild` with a
      fixed argument list (`sudo -n`, or `su -l` on managed devices). That
      script starts from `env -i`, runs the installed `gjallarctl system
      check`, builds `gjallarctl` from the checkout and runs `gjallarctl
      system rebuild`. The root side never reads `GJALLAROS_REPO`, the
      remembered state file, the working directory or `--repo`; `--host`,
      `--update-inputs` and nixos-rebuild arguments are refused, except
      `--override-input oddc <commit>`. Before building it checks: origin is
      the official URL, HEAD is on one of its branches, no tracked file
      differs from HEAD, and `flake.lock` differs from HEAD at most in the
      oddc pin, which must then be on an official ODDC branch. It prints the
      revision, its origin branches and the oddc pin.
      Plane 169: an oddc override or moved pin must be reachable from the
      official `stable`, `staging` or `verify/*` branches, checked in a bare
      partial clone (`/var/cache/gjallar/oddc-official.git`, or the user's
      cache for development checkouts) with lazy fetch off, so a commit that
      exists only in a fork is never fetched. Fails closed in both modes.
      Still open: the built revision in `system.configurationRevision`,
      applied local changes (3b), the 169 notice while the deployed oddc is
      not the pinned one, and the user-side writers of `user.config.json`
      (add-ons, USB, Secure Boot), which fail on a system checkout until
      checkpoint 3. Creating the checkout is checkpoint 4.
      Done in 0bf0c1be and 76b1646b, tested on the fw13 VM.
- [ ] **Checkpoint 3: settings service.** Root service with a typed
      D-Bus API over the `user.config.json` schema, tiers, reading and
      security delay, signed export and import, per-user device keys, key
      wipe on user removal and factory reset. The settings editor and
      `gjallarctl` use it. A Noctalia palette (loophole 21) becomes checked
      colour values instead of a file path root reads.
      Done: the palette as checked colours (d6f94109) and a tier on every
      `user.config.json` key (34ea9a04). Next: the service itself (3.3),
      signed export and import (3.4), then the user-side writers (3.5).
- [ ] **Checkpoint 3b: developer mode.** `~/GjallarOS-local/`, the copy
      into `/etc/gjallar/local`, the `local` flake input, notice, boot
      entry name and the recovery exit.
- [ ] **Checkpoint 4: installer, README, migration.** New installs clone
      into `/etc/gjallar/system`. Existing installs: one privileged step
      re-clones the official origin at the revision the system runs,
      carries `generated/` and `user.config.json` over after a check, and
      lists local commits in the old checkout so they can move into
      `~/GjallarOS-local/`. When
      `/etc/gjallar/repository` names an empty or missing path, say "The
      checkout at <path> is empty or missing" and how to fix it. Tested on
      the fw13 VM first.

### Editing on standalone devices

Jade's direction (2026-10-18): only standalone (not JODS-managed) devices
may change the system; ideally through an editor where people tick options
and add entries, with an import/export file. The worry: a file anyone can
load and activate is the most attractive target on the device.

How others handle the same split:

- **Typed settings services.** GNOME does not edit system files for the
  hostname, clock or locale. It calls small root daemons
  ([timedated](https://www.man7.org/linux/man-pages/man5/org.freedesktop.timedate1.5.html),
  hostnamed, localed) that take one typed value per call, each call its own
  polkit action. The
  [GNOME 3.4 announcement](https://gitweb.mageia.org/web/archives/plain/zarb-ml/mageia-dev/attachments/20120127/027d9fd7/attachment.mht)
  moved GNOME to these APIs. The caller never hands root a file.
- **A GUI over the NixOS configuration.** SnowflakeOS ships
  [NixOS Configuration Editor](https://discourse.nixos.org/t/nixos-conf-editor-a-gtk4-libadwaita-configuration-editor/19426),
  which edits option values in `configuration.nix` and then rebuilds behind
  an admin prompt. It writes Nix source, so anything that can feed it a
  value is writing code root evaluates.
- **Approval for system changes even for admins.** A
  [Silverblue proof of concept](https://github.com/rohanssrao/silverblue-privesc)
  showed that `rpm-ostree install` of a local package needed no password
  for wheel users, so any program in their session reached root. Fedora's
  answer, the
  [unprivileged updates change](https://fedoraproject.org/wiki/Changes/UnprivilegedUpdatesAtomicDesktops),
  lets everyone update from the official source without a password and
  asks for one for every other change, admins included.
- **Imported settings files.** Apple configuration profiles are exactly
  the "load a file, activate it" feature, and they are a known phishing
  vehicle: a profile sent as a "compliance requirement" sets a proxy, a
  root certificate or a management server
  ([NetSPI](https://www.netspi.com/blog/technical-blog/mobile-application-pentesting/malicious-mobile-configs/),
  [Jamf](https://www.jamf.com/blog/malicious-profiles-come/)). Apple's
  response is friction: opening a profile does not install it; the user
  has to find it in System Settings, sees an "unsigned" warning, and
  security-sensitive payloads only work from user-approved management
  ([Apple](https://support.apple.com/en-us/guide/mac-help/mchl42f58b2c/10.14/mac/10.14)).
- **Code changes as a visible mode.** ChromeOS lets people change the
  system only in
  [developer mode](https://chromium.org/chromium-os/developer-library/guides/device/developer-mode),
  which wipes user data on the way in and out and which an organisation
  can block for its devices.
- **The file format itself.** YAML loaders ran code or exhausted memory
  from a crafted file: PyYAML
  [CVE-2017-18342](https://nvd.nist.gov/vuln/detail/CVE-2017-18342),
  SnakeYAML [CVE-2022-1471](https://nvd.nist.gov/vuln/detail/CVE-2022-1471),
  alias expansion in Kubernetes
  [CVE-2019-11253](https://nvd.nist.gov/vuln/detail/CVE-2019-11253).

What follows for GjallarOS:

1. **Settings are data, never Nix.** The editor and any import file only
   set known keys with typed values from the `user.config.json` schema
   (catalogue app IDs, booleans, enums, checked strings). Nothing a user or
   a file supplies becomes Nix source. A root service checks each value
   against the schema, the way timedated checks a timezone, and writes
   `user.config.json` in the system checkout. Unknown keys are refused.
2. **Three tiers of settings** with their own password and delay rules
   (below).
3. **Import only signed files.** An exported file is signed by a device
   key GjallarOS manages for the user; unsigned or unknown files are
   refused, and even a valid one only loads as a draft (below).
4. **Code changes are developer mode** with a local folder outside the
   official checkout (below). JODS-managed devices cannot turn it on.
5. **Managed devices.** JODS policy decides which everyday keys the user
   may change; security keys and developer mode are never user-editable.

Residual, unchanged by this: on a wheel desktop, code in the user's
session can fake the editor's diff or catch the password through the
shell or `$PATH`. Fedora and Apple carry the same residual. Logged on the
security review page.

### Tiers, password and delay

Decided with Jade 2026-10-18: security changes show old and new value,
ask the password every time, and wait. Every setting in the schema gets a
tier; a new setting without one fails the schema check.

| Tier | Examples | Rule |
| --- | --- | --- |
| Everyday | apps from the catalogue, extra nixpkgs packages, theme, layout, displays, Wi-Fi choices | one password per apply |
| Security | AI endpoint, VPN and Tailscale, SSH keys and sshd, firewall, USB and Bluetooth policy, browser policies, DNS and NTP servers, extra root certificates, user accounts and admin rights | old -> new with a one-line effect; password every apply, never cached; Apply enabled after a 10 s reading delay at home, after a 1 h security delay elsewhere |
| Critical | update origin and channel, developer mode on, Secure Boot, LUKS and TPM PIN options, recovery off, leaving JODS | always the 1 h security delay, at home too; password when asking and again when applying |

The tier of each key is a `tier` tag on its field in
`internal/installer/config/config.go`; `config.TierOf` reads it and a test
fails when a field has none. Decided with Jade 2026-10-19:

- **Installer-only keys.** `unattendedInstall`, `autoReboot`,
  `runUpdateChecks`, `writeConfig`, `runRebuild` and `forceRedeploy` steer
  one installer run. They get the tier `installer`: the editor never shows
  them and the service refuses to change them.
- **Identity is security.** `hostname` and `username` show old -> new and
  wait. A name has to stay unique: the service refuses a new hostname that
  a peer on the user's tailnet or a device on the current network already
  uses, and says which one.
- **Every service address is security**: the AI endpoint, Nextcloud, Plane,
  draw.io and every web application, with their on/off switches. A changed
  address can lead to a fake login page, so it shows old -> new and waits.
- **Firefox add-ons are security**, both the collections and pinned store
  add-ons, because the browser installs them by policy.
- `debugFunctions` (boot menu and diagnostics) and `dotfilesDir` are
  security; `firmwareFlashReadEntry` and `secureBootPrompt` are critical
  with Secure Boot.

- **Reading delay.** Firefox keeps the Install button of add-on prompts
  disabled for a moment so a click meant for the page cannot land on it
  ([bug 162020](https://www.bugzilla.mozilla.org/show_bug.cgi?id=162020));
  the delay restarts whenever the window loses focus, which closed later
  bypasses ([MFSA 2016-08](https://www.mozilla.org/en-CA/security/advisories/mfsa2016-08/)).
  GjallarOS uses 10 s because the point is reading the change, not only
  avoiding a stray click.
- **Security delay.** Apple's Stolen Device Protection makes some changes
  wait an hour and ask Face ID again when the phone is away from familiar
  places ([HN discussion](https://news.ycombinator.com/item?id=39093978)).
  The weak spot people found: "familiar" was learned from location history,
  so a bar someone visits often counted as home
  ([9to5Mac](https://9to5mac.com/2024/01/28/security-bite-iphones-stolen-device-protection-has-a-fatal-flaw-but-you-can-fix-it/)).
  GjallarOS uses the proven home network instead (a router of the user's
  own tailnet, see `routerHome`), and critical changes ignore location.
- **While a change waits.** A notification names the change and offers
  Cancel. Locking the screen does not cancel it; logging out or rebooting
  does. When the hour is over, the password is asked again and the change
  applies. The wait and the result go to the journal, and to JODS on
  managed devices.
- **Recovery is the trusted path.** The recovery system runs outside the
  user's session, so it may apply security and critical changes without
  the delay, after the admin password. That is also how a person who is
  locked out fixes things.
- **Managed devices.** Security and critical keys come only from JODS;
  the local editor shows them read-only.

### Settings service (step 3.3)

Decided with Jade 2026-10-19: Apply also starts the rebuild, so one
password covers it and the build is exactly the approved change.
`gjallarctl settings` comes first; the graphical editor is its own job on
the same API. A device with no proven home (no Tailscale, or no router of
the user's tailnet on the network) always waits the hour. Once a home
router is proven during the wait, Apply opens after the 10 s reading delay.

- **`gjallar-settingsd`** owns `org.gjallaros.Settings1` on the system bus
  and starts on demand. Small typed methods:
  - `List` gives every key with its tier; `Get` one value.
  - `Request` takes key -> JSON value pairs. Each key must exist and must
    not be `installer`; each value must decode into the key's type, and the
    whole result must pass the same validation as the installer. Root
    stores old and new value, tier, the caller's user and session, and the
    earliest apply time, and gives back an id. Critical changes ask the
    password here already.
  - `Apply` takes that id, never values again, and the checked Noctalia
    colors of the caller (loophole 21 rule). It asks the password, checks
    the wait, refuses when a stored old value no longer matches the file,
    writes `user.config.json` atomically and starts the rebuild unit.
  - `Pending`, `Cancel`, and signals for a finished wait and rebuild.
  A request that mixes tiers follows the strictest one.
- **Polkit.** One action per tier (`org.gjallaros.settings.everyday`,
  `.security`, `.critical`), `auth_admin` without `_keep`. Root passes the
  keys and an "old -> new" message in the details, so the password dialog
  shows what root will write, and polkit rules can match single keys,
  which is how JODS can lock keys per device later.
- **Waits** are kept by the service, not by polkit, so a rule that drops
  the password still keeps the delay. Pending changes live in
  `/run/gjallar/settings` (gone at reboot); the service drops a change when
  the logind session that asked for it ends. Locking keeps it. Home is
  root's own verdict in `/run/gjallar/vpn-trust.json`, and only `home`
  counts; a trusted Wi-Fi does not. Critical changes ignore home.
- **Managed devices** get security and critical keys from JODS only; the
  service refuses them locally.

### Settings service security pass

Threat model: code running as the user (a hostile program in the session,
a browser exploit) wants a security key changed, or wants root through the
service. Out of scope: an attacker who is already root, firmware attacks.

What broke elsewhere, and the answer here:

- **Caller identified by PID.** polkit's `unix-process` subject let a
  caller swap the process behind a PID
  ([CVE-2013-4288](https://bugzilla.redhat.com/show_bug.cgi?id=1002375));
  InputPlumber made the same mistake in 2026
  ([SUSE](https://security.opensuse.org/2026/01/09/inputplumber-lack-of-dbus-auth.html)).
  The service only uses the `system-bus-name` subject.
- **Caller leaves the bus during the check.** polkit treated such a caller
  as authorized
  ([CVE-2021-3560](https://thehackernews.com/2021/06/7-year-old-polkit-flaw-lets.html)).
  Any error, or a caller whose user cannot be read from the bus, is a
  refusal.
- **Checks that do not gate anything.** SUSE found services that ask
  polkit and carry on regardless, generic methods that pick an operation
  from a string, one action for everything, root opening caller-supplied
  paths, and early decoding of untrusted blobs
  ([SUSE, KDE 6 review](https://security.opensuse.org/2024/04/02/kde6-dbus-polkit.html)).
  Here: one code path where the polkit result gates the write, one action
  per tier, no paths in the API, values decoded into the typed field with
  unknown fields refused and a size limit per request.
- **Shown is not applied.** If Apply took values, a program could show one
  change and apply another. Apply takes the id of what root stored and
  re-checks the old values.

Residual, unchanged: the polkit agent and the terminal run in the user's
session, so code there can fake the dialog. Logged on the security review
page.

### Signed settings files

Decided with Jade 2026-10-18: imports must be signed, and GjallarOS does
the key handling unless the installer is told otherwise.

Inspiration: systemd-homed signs every user record and only accepts records
whose signature matches a public key installed on the machine; moving a
user to another machine means copying that public key
([systemd-homed.service(8)](https://man7.org/linux/man-pages/man8/systemd-homed.service.8.html)).
A point from the
[HN discussion of homed](https://news.ycombinator.com/item?id=22194910):
check the signature over the raw bytes before parsing anything. Apple
profiles show "not signed" but still install, which is what phishing uses.

- **Device key per user.** The settings service creates an Ed25519 key
  pair when a user account is created (installer or new user). The private
  key stays with the service, root-only and sealed with `systemd-creds`
  (TPM where there is one). The user never sees or handles it.
- **Export.** The user picks Export; the service asks for the user's own
  password, signs the exact file bytes (with schema version, device,
  user and time inside) and hands back the file.
- **Import.** The service checks the signature over the raw bytes first,
  then parses with the strict parser. Accepted signers: the users of this
  device, devices the user paired, and on managed devices the JODS
  organisation key. Anything else is refused with "This file was not made
  on this device or a device you paired". A valid file still only loads
  as a draft, with the usual tier rules: a signature says where the file
  came from, not that the change is wanted now.
- **Pairing own devices.** Both devices show the same short code; the
  user confirms on both with their password. Each then trusts the other's
  public key for that user. Unpairing removes it.
- **Opting out in the installer.** "Let GjallarOS manage signing keys
  (recommended)". Saying no lets the person add their own public key (for
  example an SSH or hardware key) as the only signer instead.
- **Nothing is locked behind the key.** Login, disk unlock, applying
  settings in the editor and recovery never need it. Losing it only makes
  old exports unimportable.
- **Reset and recovery.** Removing a user (editor or recovery) deletes
  that user's key and the pairings made for them. A factory reset from
  recovery deletes all keys, pairings and the `systemd-creds` host secret,
  so a copied key file cannot be unsealed later. A new user always starts
  with a fresh key. Leaving JODS removes the organisation key.

### Developer mode and the local folder

Decided with Jade 2026-10-18: Nix code changes only in developer mode, so
nobody breaks their system by accident and it is always clear what caused
a problem.

- **Turning it on** is a critical change: an explanation screen, the
  password, the 1 h delay, the password again. A notice stays in the bar
  while it is on. JODS devices cannot turn it on.
- **The folder.** Turning it on creates `~/GjallarOS-local/` from a
  template: `system/` for NixOS modules, `home/` for home-manager modules,
  a README. Every `.nix` file there is imported; a name starting with `_`
  is skipped. No git needed, and the official checkout is never touched,
  so pulls never conflict.
- **Apply.** `gjallarctl system apply` (or the editor button) copies the
  folder as data into root-owned `/etc/gjallar/local`: `.nix` files and a
  short list of asset types only, no links, no hidden files, a size cap.
  It shows the file diff with the security-tier rules, then builds with
  the flake's `local` input pointed there. With developer mode off, that
  input is an empty stub in the repository.
- **Visible everywhere.** Each generation records the folder's hash; the
  boot entry says "local changes"; `gjallarctl system status` lists the
  files. Later: compare the security-relevant options of the official and
  the local build and list what local code changed (firewall, sshd, ...).
- **Getting out.** If a local module breaks the system, the previous
  generation boots; recovery offers "Turn off developer mode and rebuild
  the official system". Turning it off keeps `~/GjallarOS-local/`.
- **Adding an app does not need it.** Extra nixpkgs packages are an
  everyday setting (attribute names checked against the pinned nixpkgs).

Honest limit: in developer mode the local folder is root code. The delay,
diff and notice make that a visible choice; they do not make local code
safe.

### Decision

2026-10-18, Jade: typed settings service; security changes show old and
new value, ask the password every time and wait; imports only signed, with
GjallarOS managing the keys by default; Nix code only in developer mode
with a local folder. Migration on the one existing install: whatever does
not break the running system. New installs get no `~/Documents` clone.
