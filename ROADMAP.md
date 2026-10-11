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
      `flake.lock` pins. `rebuild --hardware-update --stage main|staging`
      keeps working as it does today and is not tied to the GjallarOS
      channel.
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
