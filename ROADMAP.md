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

### A mounted card at the first arming

Found in the fw13 VM on 2026-10-19: the rebuild that switches
from audit to blocking pulled a mounted card (SYNTHETIC image). Jade's rule
(2026-10-19): the first arming keeps only a device that has nothing but
mass-storage interfaces and whose medium is mounted. Everything else asks.
Boot and a broker restart still block everything.

**Why it broke.** The nixpkgs module writes `ImplicitPolicyTarget` into the
daemon config in the store, so switching `enforce` changes usbguard's
`ExecStart`, and the rebuild restarts usbguard. On start, usbguard applies
`PresentDevicePolicy=apply-policy` to every device already plugged in
([RHEL 7 Security Guide](https://docs.redhat.com/en/documentation/Red_Hat_Enterprise_Linux/7/html/Security_Guide/sec-Using-USBGuard.html)).
With no rules and the block fallback, that blocks the reader before the
broker or the media gate get a say. The gate's own keep never ran on a live
reader. It also logs `kept-open` after every broker restart, because it
treats the first poll of a process as an arming.

**How others do it.** RHEL tells admins to run `usbguard generate-policy`
before enforcing, which allows every present device for good, keyboards
included ([RHEL 7](https://docs.redhat.com/en/documentation/Red_Hat_Enterprise_Linux/7/html/Security_Guide/sec-Using-USBGuard.html),
[Red Hat blog](https://www.redhat.com/en/blog/usbguard-improvements-red-hat-enterprise-linux-83)).
GNOME's USB protection only blocks devices plugged in later
([gsd MR 75](https://gitlab.gnome.org/GNOME/gnome-settings-daemon/-/merge_requests/75),
[Arch wiki](https://wiki.archlinux.org/title/USBGuard)). Jade's rule is
narrower than both: one mounted storage device, for this connection only.

**Security pass.**
- What a kept device can do: it has only class 08 interfaces, so it cannot
  type or act as a network card. Its contents were already mounted and
  read during audit, so keeping it adds no new exposure.
- How storage turned hostile in practice: BadUSB firmware made a stick
  pose as a keyboard ([SR Labs, Black Hat 2014](https://techxplore.com/news/2014-07-sr-labs-expose-badusb-week.html)).
  A device that adds interfaces has to enumerate again, and usbguard then
  treats it as a new insertion, which is blocked once armed. Composite
  devices with storage plus anything else are never kept.
- The keep grants nothing the owner could not grant by hand: only devices
  whose review offers "Allow until unplugged" qualify, never debug-class
  parts. The allow lives in broker memory. Unplug, reboot or broker restart
  ends it.
- Fail closed: the keep is part of the signed arm step. If arming fails
  (no TPM key, a failed check) nothing is kept and everything is blocked
  as before. If the gate cannot list mounts, nothing is kept.
- A new gap from not restarting usbguard: until the new broker's first
  pass, usbguard still runs with the audit fallback. The broker therefore
  sets the block fallback before it does anything else, and present devices
  keep their audit state only until the same pass applies the policy.
- A medium swapped in a kept reader is closed and asks, as before.

**Decision.**
- [x] usbguard reads its daemon config from `/etc/usbguard`, and only the
      fallback line depends on `enforce`. Every other line is a restart
      trigger, so real config changes still restart usbguard. The mode
      switch no longer does: the broker already sets the fallback at runtime.
- [x] The arm step keeps a present device when all its interfaces are
      class 08, the gate reports one of its LUNs mounted, and its review
      offers "Allow until unplugged". It gets that choice for this
      connection.
- [x] The media gate keeps open only on the controller's real arm in this
      process, never on its first poll after a restart.
- [x] fw13 VM: disarm, audit rebuild, mount a SYNTHETIC card, enforce
      rebuild → card stays mounted; reinsert asks; broker restart blocks it
      with no `kept-open`; a keyboard present at arming is blocked.
      PASS fw13 VM 2026-10-19: rebuild arm 16:38 and the delayed
      `gjallarctl settings apply` arm 19:10 both kept the SYNTHA card mounted
      (`kept-open`), usbguard not restarted; the keyboard was blocked; a
      broker restart and a reinsert both blocked the card and asked.
- Open: booting with `enforce` off while the signed state is still armed
  starts usbguard with the allow fallback until the broker blocks
  (loophole 23).

### Trust lapse after N days unused

Permanently trusted external devices can lapse. A device that has not been
connected for N days asks again on its next connection. It is off by
default for standalone machines; the installer asks, and N is 30 when it is
on. JODS will set it for managed machines later.

How others do it:

- Android revokes a remembered adb host after 7 days without a connection
  (`DEFAULT_ADB_ALLOWED_CONNECTION_TIME`). The clock restarts on every
  connection, a daily job removes expired keys, and 0 turns it off.
  [AdbDebuggingManager.java](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/services/core/java/com/android/server/adb/AdbDebuggingManager.java)
- macOS on Apple silicon asks before a new accessory connects. Approved
  accessories keep working on a locked Mac for three days after it was last
  locked; after that the Mac asks to be unlocked first.
  [Apple deployment guide](https://support.apple.com/guide/deployment/depf8a4cb051/web)
- GrapheneOS blocks new USB connections once the phone is locked
  ("Charging-only when locked" is the default); connections that already
  exist keep working until they end.
  [GrapheneOS features](https://grapheneos.org/features)
- USBGuard and bolt keep allow rules until someone removes them. Neither
  has an expiry, and the USBGuard tracker has no request for one.
  [usbguard(1)](https://man.archlinux.org/man/usbguard.1.en),
  [boltctl(1)](https://manpath.be/f39/1/boltctl)
- NIST SP 800-53 AC-2(3) disables accounts, device identifiers included,
  after a set time without use. Agencies pick 15 to 35 days.
  [AC-2(3)](https://csf.tools/reference/nist-sp-800-53/r5/ac/ac-2/ac-2-3/),
  [DISA STIG V-223584](https://www.stigviewer.com/stigs/ibm_zos_acf2_v9/2025-06-24/finding/V-223584)

Jade decisions (2026-10-19):

- N counts from the last time the device was connected, not from the last
  approval. Devices in daily use never ask, forgotten ones lapse, and there
  is no monthly "click Allow" habit to train.
- A device whose trust lapses while it is plugged in keeps working until it
  is unplugged or the machine restarts.
- Three days before a device lapses, a notification offers to renew it.
- Only external devices lapse. Built-ins enrolled from ODDC never lapse;
  they are checked against ODDC on every boot.
- The clock starts when lapse is turned on (`lapseSince` in the signed
  record). Turning it on does not make every old device ask at once.
- Turning lapse on or lowering N applies at once. Turning it off or raising
  N waits out the security delay.
- Without a trusted built-in USB keyboard, the most recently used external
  keyboard and mouse (boot interfaces 03:01:01 and 03:01:02) never lapse.
  Other input devices do. With a built-in USB keyboard, every external
  device lapses.

Security pass:

- **What it protects against.** Stale trust: a stick or dock trusted once
  and forgotten, then lost, lent out, or copied later. It does not stop a
  copy of a device that is in daily use; identity strength covers that.
- **Where the time comes from.** Each record carries a TPM-signed
  `lastUsed`. The broker refreshes it at most once a day while the device is
  connected and matches its trust. Old records without it use
  `lastAccepted`. Nobody can move it without the signing key.
- **Clock set back.** A `lastUsed` more than a day in the future means the
  clock went back or the record is wrong. That device lapses and asks; it
  does not get extra days. Setting the clock back by less than a day buys
  at most that day. Setting the clock needs root or an admin's polkit
  approval; a spoofed NTP reply is the remote path, which the VPN-aware NTP
  work is meant to close.
- **Clock set forward.** Devices lapse early and ask again. That fails
  closed. Re-allowing writes the real time, so a bad clock does not poison
  later records.
- **Broken signing.** If the TPM key cannot sign, `lastUsed` stops moving
  and devices lapse. That fails closed, and the broker logs why.
- **Re-allowing.** A lapsed device gets the same review as a changed one:
  keep blocked, allow once, or renew. Renewing keeps the record and its
  first-accepted time and needs the same fingerprint or password as any
  other trust change. Allowing it once does not renew it.
- **Renewing ahead.** The three-day notice renews a device that is not
  connected. The broker accepts that only inside the last three days before
  the lapse, never after it, and only with the same fingerprint or password
  as any other trust change. A device that has lapsed has to be connected
  and reviewed, so its identity is checked again.
- **Turning it off.** Switching lapse off or raising N lowers protection.
  The settings service holds that change like it holds turning blocking
  off. Turning it on or lowering N only tightens, so it applies at once. On
  a JODS-managed machine both directions stay with JODS.
- **Turn-on time.** `lapseSince` sits in the TPM-signed document, so it
  cannot be moved back to make devices lapse, or forward to keep them.
  A `lapseSince` more than a day in the future counts as lapsed, like
  `lastUsed`.
- **Lockout.** The kept keyboard and mouse are picked only from devices
  that are already trusted, so keeping them adds no new trust. A
  keyboard built in over PS/2 or i2c is not in the USB record, so laptops
  keep their last USB keyboard and mouse too for now. That is the safe side.
  Asking ODDC whether its device list can name a non-USB built-in keyboard
  is open; the kernel's own list was ruled out because desktops show a
  phantom i8042 keyboard.

Plan:

- [x] C1: `lastUsed` in the trust record, lapse check in the audit
      (`trusted-external-lapsed`, offers renew), lapse ignored for a
      connection that was trusted when it started, broker refreshes
      `lastUsed` once a day (twice per lapse when shorter).
- [x] C2: `--lapse-days N`, Nix
      option `services.gjallar-usbtrust.lapseDays` (null = off), status
      shows when each device lapses.
- [x] C3: warning three days ahead (half the lapse when shorter) with a
      Renew button (`gjallarctl usb renew`, accepted only in that window);
      no warning while the device is plugged in.
- [x] C4: `usbTrustLapseDays` setting (held when it lowers protection),
      installer question (default off, 30 when on, any N from 1), clock
      starts at turn-on, last-used keyboard and mouse kept.
- [x] C5: fw13 VM test (2026-10-19, 388245b0).

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
      - Jade decisions (2026-10-19): only on machines with TPM2. The try
        keeps the firmware's keys and adds ours, runs the option ROM check
        from the event log, and shows generic setup-mode steps. The recovery
        partition and the disk shrink happen only after the first verified
        Secure Boot start, with the same marker for the first run and a
        rerun. On failure Secure Boot goes off, and `gjallarctl secure-boot
        setup` can try again later. Research flow and security pass first.
- [x] **Secure Boot before recovery.** The installer asks about Secure Boot
      first and about the recovery partition only when Secure Boot is on.
      Without it there is no recovery partition and no shrink, for presets
      too. ODDC models default to yes. Saying no lists what the install goes
      without in plain words.
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

Plan agreed 2026-10-10. Channels decided 2026-10-20 (Q16): `main` became
`dev`, the GitHub default branch is `stable`, new installs follow `stable`.

- [x] **Three channels.** `dev` replaced `main`. `staging` and `stable` are
      branches that only ever fast-forward to a tag `vYYYY.MM.N` (N counts
      the releases in that month). A tag never moves; a fix found in
      staging gets a new tag. Tags are signed and annotated once the key
      talk (Q4) is done; until then they are lightweight.
- [ ] **Release flow.**
      1. A branch bumps all flake inputs (nixpkgs, Home Manager, Noctalia,
         the ODDC pin and the rest).
      2. The full `.vm/installer-e2e/REGRESSION.md` runs in VMs.
      3. Merge to `main` and tag.
      4. `staging` moves to the tag. The Framework 13 and the ZBook x2 G4
         run it for a while.
      5. `stable` moves to the same tag.
- [x] **Picking a channel.** `user.config.json` has `releaseChannel`
      (`dev`, `staging` or `stable`; a critical setting, except that a move
      to `stable` applies at once). New installs and older configurations
      get `stable`. The installer clones the installed commit, which must be
      on one of the three branches, onto a local branch that tracks the
      chosen channel. `rebuild --update` fetches origin and only
      fast-forwards to the channel. A checkout ahead of its channel stays
      where it is until the channel catches up, so a move to an older
      channel never downgrades. A checkout on the old `main` branch or on
      another channel's branch moves to the channel's branch on its next
      update; any other branch stops the update.
- [ ] **Channel follow-ups.** The signature check below before the
      fast-forward. A bar notice when the channel is ahead. JODS sets the
      channel on managed devices.
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
      Each build records its revision in `system.configurationRevision`
      and in `/etc/gjallar/build-source.json` with the deployed and pinned
      oddc commits. While they differ, a notice comes at every login until
      a plain rebuild returns to the pin. Still open: applied local changes
      (3b). Creating the checkout is checkpoint 4 (done).
      Done in 0bf0c1be, 76b1646b and job 169, tested on the fw13 VM.
- [ ] **Checkpoint 3: settings service.** Root service with a typed
      D-Bus API over the `user.config.json` schema, tiers, reading and
      security delay, signed export and import, per-user device keys, key
      wipe on user removal and factory reset. The settings editor and
      `gjallarctl` use it. A Noctalia palette (loophole 21) becomes checked
      colour values instead of a file path root reads.
      Done: the palette as checked colours (d6f94109), a tier on every
      `user.config.json` key (34ea9a04), the service itself with its
      `gjallarctl settings` front end (3.3, 887c1c5b) and the unique
      hostname check, signed export and import for the device's own users
      (3.4a), key wipe on user removal (3.4b), pairing two devices of one
      person with a file each way and a code (3.4c, fbf8cc67), own
      OpenSSH keys with the installer question (3.4d, 46a02b61, fw13 VM
      passed), and the user-side writers (add-ons, USB review, Secure
      Boot) asking the service on a system checkout, with each inner value
      of a change listed (3.5, e511f5cb).
      Next: factory reset waits for a recovery mode that does one.
- [x] **Checkpoint 3b: developer mode.** `~/GjallarOS-local/`, the copy
      into `/etc/gjallar/developer/local`, the staged `.gjallar-local/`, notice,
      boot entry name and the recovery exit. Steps: 3b.1 the
      `developerMode` key (critical on, everyday off, refused with JODS),
      the folder checks and the staged import with hash check and
      `local-changes` tag; 3b.2 the root copy through the settings
      service with the file diff; 3b.3 the CLI, template and status
      listing; 3b.4 the bar notice; 3b.5 the recovery exit. Done: 3b.1
      (81cac146, 68eed82a), 3b.2 (b1a07ba9), 3b.3 (d949e67c), 3b.4
      (8415af43; the key reaches `state.nix` ecaca469, whole tooltip
      6a70e39d), 3b.5 (47731eb4: `gjallar-recover developer-off` runs
      the root-only `gjallarctl system developer-off`, which passes only
      when the next boot holds no local files, 0b1704c7; rebuilds in the
      recovery entry start with the next boot). fw13 VM template run
      2026-10-20 (e8c6543e).
- [x] **Checkpoint 4: installer, README, migration.** New installs clone
      into `/etc/gjallar/system`. Existing installs: one privileged step
      re-clones the official origin at the revision the system runs,
      carries `generated/` and `user.config.json` over after a check, and
      lists local commits in the old checkout so they can move into
      `~/GjallarOS-local/`. When
      `/etc/gjallar/repository` names an empty or missing path, say "The
      checkout at <path> is empty or missing" and how to fix it. Tested on
      the fw13 VM first.
      Done: 4.1 installer clone (cb6de68f), shared clone code (e192375c),
      4.3 `gjallarctl system move` (user, lists what stays behind) and
      `sudo gjallarctl system clone --from DIR` (root, clones the running
      commit, copies the machine-local files as data, never runs git in
      DIR) in f3822087, 4.2 missing-checkout message (77525b5c), 4.4
      README. Tested on the fw13 VM (2026-10-20, 0b1704c7): missing
      checkout message, move, clone, second clone refused, the rebuild
      switching the pointer. The live install moves after its commits
      are on origin (Q9).

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
  the password still keeps the delay. Pending changes live only in the
  service's memory, so root never parses a state file and a restart or
  reboot drops them; the service also drops a change when the logind
  session that asked for it ends. Locking keeps it. Home is
  root's own verdict in `/run/gjallar/vpn-trust.json`, and only `home`
  counts; a trusted Wi-Fi does not. Critical changes ignore home.
- **Managed devices** get security and critical keys from JODS only; the
  service refuses them locally.
- **Unique hostname** (decided with Jade 2026-10-19). Tailscale gives a
  clashing machine a `-1` name that sticks
  ([machine names](https://tailscale.com/kb/1098/machine-names)), and mDNS
  renames on a conflict ([RFC 6762](https://rfc-editor.org/rfc/rfc6762)
  sections 8 and 9), so a clash is better refused up front. The config check
  holds `hostname` to the NixOS label rule. `Request` and again `Apply`
  refuse a new name that a tailnet peer uses as host or MagicDNS name,
  shared peers included, or that someone answers for as `<name>.local` on
  the current network; letter case does not count and the answer names the
  peer or address. The network is asked only when root's verdict is home,
  site or trusted Wi-Fi; elsewhere a query would announce the name to
  strangers, so the answer carries a note instead. A check that fails
  refuses the change. The mDNS question is a legacy unicast query (section
  6.7) for A and AAAA, three times within a second, asked from the fixed
  port 1015 (IANA keeps 1011-1020 reserved). The firewall lets in answers
  to that port only from port 5353 and from private and link-local
  addresses (decided with Jade 2026-10-19, see the security pass below).

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
- **Root reading the network.** mDNS answers come from anyone on the
  link, and DNS parsers have a long record of bugs in name decompression
  ([NAME:WRECK](https://www.forescout.com/research-labs/namewreck/)).
  `gjallar-settingsd` keeps `PrivateNetwork` and never reads them: a
  socket-activated `gjallar-namecheck` runs per question as a dynamic user
  whose only capability is `CAP_NET_BIND_SERVICE`, may only reach multicast
  and local addresses, and answers one JSON line over a root-only socket.
  Its parser only follows compression pointers backwards and stops at 255
  bytes.
- **Answers the firewall drops.** The first version asked from a random
  port and found every name free: a unicast answer to a question sent to a
  multicast group is not part of any connection conntrack knows, so
  `nixos-fw` dropped it. This is old and known
  ([netfilter bug 795](https://lists.netfilter.org/pipermail/netfilter-buglog/2012-June/002447.html),
  [OpenWrt forum](https://forum.openwrt.org/t/transforming-an-udp-broadcast-packet-to-a-unicast-packet-to-traverse-subnets/114381/22));
  a conntrack helper for it was never taken upstream
  ([SUSE SLP patch](https://code.opensuse.org/kernel/kernel-source/blob/SLE12-SP5/f/patches.suse/netfilter-ip_conntrack_slp.patch)),
  and the usual advice is to open UDP 5353
  ([ArchWiki, systemd-resolved](https://wiki.archlinux.org/title/Systemd-resolved#mDNS)).
  A check that cannot hear answers fails open, so Jade chose a fixed port
  with a narrow rule (2026-10-19): the helper asks from port 1015, and the
  firewall accepts only UDP from port 5353 to port 1015 from private and
  link-local addresses. A port below 1024 needs the capability, so no local
  program can sit on it and read or fake answers; the helper therefore has
  no `PrivateUsers`, whose capabilities do not count for host network
  binds. The service runs one check at a time and the socket takes one
  connection. Nothing listens on the port outside a check. Tested on the
  fw13 VM with a SYNTHETIC responder in a second network namespace: its
  answer counts, answers from another port or a public address do not.

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
- **Pairing own devices.** A pairing file goes each way; both devices
  show the same code made from both public keys, and the user confirms on
  both with their password. Each then trusts the other's public key for
  that user. Unpairing removes it. See "Pairing and own keys" below.
- **Opting out in the installer.** "Let GjallarOS manage signing keys
  (recommended)". Saying no lets the person add their own OpenSSH public
  key (ed25519 file or FIDO hardware key) as the only signer instead.
- **Nothing is locked behind the key.** Login, disk unlock, applying
  settings in the editor and recovery never need it. Losing it only stops
  export until a new one is made; old exports still import, because the
  public keys stay.
- **Reset and recovery.** Removing a user (editor or recovery) deletes
  that user's key and the pairings made for them. A factory reset from
  recovery deletes all keys, pairings and the `systemd-creds` host secret,
  so a copied key file cannot be unsealed later. A new user always starts
  with a fresh key. Leaving JODS removes the organisation key.

How 3.4a does it (`internal/settings/signed.go`,
`cmd/gjallar-settingsd/keys.go`):

- **File.** Three header lines, `gjallaros-settings-v1`, `key <public
  key>` and `sig <signature>`, then the JSON. The signature covers every
  byte but the sig line. Only Ed25519; the format has no algorithm field.
  The key line only names which trusted key to check with: a key that is
  not one of this device's users' keys is refused before the signature is
  looked at. The payload holds schema, device, user, uid, time and the
  settings: every `user.config.json` key except installer keys.
- **Host key.** `gjallar-settings-key.service` makes 32 random bytes once
  and seals them with `systemd-creds encrypt --with-key=auto` into
  `/etc/credstore.encrypted/gjallar-settings-key`: TPM2 plus the host
  secret where there is a TPM, no PCR binding, so turning on Secure Boot
  keeps it. systemd hands it to the service with `LoadCredentialEncrypted=`;
  the service never touches the TPM. Without it export is off and import
  still works. The key unit runs before every start of the service and
  replaces a key that no longer decrypts. A cleared TPM or a changed
  `/etc/machine-id` loses the key; systemd deletes its host secret when
  the machine id changes. Without this check the service would fail until
  the next boot.
- **User keys.** Made on the first export. Each user's Ed25519 seed is
  HKDF-SHA256 of the host key with a random salt and the uid and name.
  `/var/lib/gjallarOS/settings/keys/<uid>.json` (root, 0600) holds the salt
  and public key; without the host key it is only a public key. A key
  stops counting once its uid has another name, so a new account on an old
  uid starts fresh. When the host key changes (a cleared TPM) the next
  export makes a new key and keeps the old public key, so earlier files
  still import.
- **Device.** The file names the device by HMAC-SHA256 of the machine id
  over a fixed app id, as `sd_id128_get_app_specific` does, so it never
  carries the machine id.
- **Export.** `gjallarctl settings export FILE`. polkit action
  `org.gjallaros.settings.export` asks for the user's own password
  (`auth_self`). The service signs what it reads itself from the checkout,
  never bytes the caller sends, so it cannot be used to sign anything
  else. The command writes the file as the user, 0600, never over an
  existing file.
- **Import.** `gjallarctl settings import FILE`. Size cap, signature over
  the raw bytes, then the strict parse. The signer's uid and name must be
  the ones in the file, the device must be this one, and the caller must be
  the account that made it. The change then goes the way of `settings set`,
  with a note saying who made the file and how many days ago.
- **Removed users (3.4b).** Each time the service reads the trusted keys,
  and once at start, it deletes the record of a uid that has no account
  or now has another name. Only a clear "no such account" counts: when the
  lookup itself fails the record stays and is not trusted for that check.
  A file of a removed user is then refused for good, even when an account
  of the same name comes back. Recovery has no factory reset yet (modes
  are repair, reinstall and fresh); one that is added must delete the key
  records, `/etc/credstore.encrypted/gjallar-settings-key` and the
  `systemd-creds` host secret.

Security pass (2026-10-19). What went wrong elsewhere, and what this
design does about it:

- Trusting the algorithm or key a message names:
  [CVE-2015-9235](https://nvd.nist.gov/vuln/detail/CVE-2015-9235)
  (jsonwebtoken). Here the algorithm is fixed and the key must already be
  trusted.
- Checking one reading of the bytes and acting on another: SAML comment
  handling, [CVE-2017-11427](https://nvd.nist.gov/vuln/detail/CVE-2017-11427)
  ([Duo PSA-2017-003](https://duo.com/blog/duo-finds-saml-vulnerabilities-affecting-multiple-implementations),
  [CERT VU#475445](https://www.kb.cert.org/vuls/id/475445)). Here the
  signature is checked over the raw bytes, and those same bytes are
  parsed; nothing is canonicalized.
- Parsers that disagree on repeated JSON names:
  [CVE-2017-12635](https://nvd.nist.gov/vuln/detail/CVE-2017-12635)
  (CouchDB). The parser refuses repeated names, unknown fields, text that
  is not UTF-8 and anything after the object.
- Signature checks that accept zero values:
  [CVE-2022-21449](https://neilmadden.blog/2022/04/19/psychic-signatures-in-java/).
  Go's `crypto/ed25519` is used as is, and a test checks that an all-zero
  signature is refused.
- Rollback: TUF names replaying old signed data as its own attack
  ([specification](https://theupdateframework.github.io/specification/latest/),
  [go-tuf CVE-2022-29173](https://nvd.nist.gov/vuln/detail/CVE-2022-29173)).
  Residual: an old file of the same user on the same device stays valid.
  It only ever brings back that user's own earlier settings, as a draft
  with the tier rules and the password, and the note shows its age.
  Logged on the security review page.

### Pairing and own keys

Decided with Jade 2026-10-20 (wiki 752 Q13, Q14): pairing is a file each
way plus a code made from both public keys, with no network listener; an
own key is an OpenSSH key used with `ssh-keygen -Y sign`.

How others do it:

- Signal shows a 60-digit safety number: two 30-digit halves, one per
  person, each an iterated SHA-512 over that person's key and identifier,
  in groups of five digits. Both phones show the same number because the
  halves are put in a fixed order
  ([moderncrypto messaging list](https://moderncrypto.org/mail-archive/messaging/2017/002381.html)).
- KDE Connect's pairing review
  ([oss-security 2020-11-30](https://www.openwall.com/lists/oss-security/2020/11/30/1),
  CVE-2020-26164): device names are chosen by the other side, and the
  fix shows a hash over both keys, but by default only 8 hex digits.
- Short key ids: 32-bit PGP key ids were collided for every key of the
  web of trust, and fake kernel developer keys turned up on key servers
  in 2016 ([LWN](https://lwn.net/Articles/697538/)). A short code that
  someone can search for offline proves little.
- OpenSSH signatures
  ([PROTOCOL.sshsig](https://github.com/openssh/openssh-portable/blob/master/PROTOCOL.sshsig))
  sign a namespace with the message hash, so a signature made for git
  (namespace `git`) or anything else never counts for another use. FIDO
  keys carry a user-presence flag, and `ssh-keygen -Y verify` refuses a
  signature without it unless the signer line says `no-touch-required`.

**Pairing (3.4c).**

- **File.** `gjallaros-pairing-v1`, then the `key` and `sig` lines as in a
  settings file, then JSON with schema, device, user, uid, time and the
  hostname. Signed by the user's own key, so it shows the sender holds it.
  The other magic line means a pairing file never opens as a settings
  file, nor the other way round.
- **Code.** One half per side, Signal-style: SHA-512 over
  `gjallaros-pair-v1`, the device id, uid, user name and key, then 5200
  more rounds over the hash and the key; the first 30 bytes give 30
  digits. The two halves are put in byte order, so both screens show the
  same 60 digits in groups of five. Changing either key changes its half,
  so a swapped file needs a match for the whole 30-digit half of a key it
  does not have.
- **Flow.** `gjallarctl settings pair export FILE` on each device (own
  password, `auth_self`, makes the key when there is none yet), then the
  files go across by any way the user likes (USB stick, Nextcloud, mail).
  `gjallarctl settings pair add FILE` shows the code and what the other
  device says it is, and asks to compare it with the other screen, at the
  same time, both devices in front of the user. A code sent by message
  proves nothing; on a difference, stop. The user then confirms with their
  own password. The service computes the code again from the bytes it gets
  and adds the pairing only when it equals the code the user saw.
  `pair list` shows pairings with a short id; `pair remove ID` drops one.
- **Store.** `/var/lib/gjallarOS/settings/pairings/<uid>.json` (root,
  0600), one per local user: the other device's key, device id, uid, user
  name, claimed hostname and when it was added. A removed user loses their
  pairings, as with keys. A key of this device, or a file naming this
  device, is refused.
- **Import from a paired device.** The signer must be one of the caller's
  pairings, and the file's device, uid and user must be the ones stored
  with it. Keys that describe the device rather than the person (struct
  tag `scope:"device"` on `config.User`: hostname, username, disk, boot,
  recovery, USB blocking, JODS, firmware, channel, developer mode,
  tailscale, dotfiles folder, clamshell) are left out and the note says how
  many. Everything else goes the way of `settings set`, with the tier
  rules, waits and password, and a note naming the paired device.
- **Key changes.** When the other device's host key changes (a cleared
  TPM), its files no longer import here; pair again.

**Own key (3.4d, done 46a02b61).**

- The installer asks "Let GjallarOS sign your settings files for you?"
  (recommended). No: the user gives an OpenSSH public key line
  (`ssh-ed25519` or `sk-ssh-ed25519@openssh.com`), checked before the
  interview goes on. It is stored as `settingsSigningKey` (critical tier,
  device scope) and belongs to the account named by `username`. That key
  is then the only signer for that account: managed files and pairing are
  refused. It can be set, changed or cleared later with the password and
  the hour's wait; the change says what it does to export, import and
  pairing.
- Export writes the settings without a signature, under its own magic
  line (`gjallaros-settings-ssh-v1`). The user signs it:
  `ssh-keygen -Y sign -n gjallaros-settings -f KEY FILE` gives `FILE.sig`.
- Import checks with `ssh-keygen -Y verify -n gjallaros-settings` from the
  Nix store path, no shell, a cleared environment, a size cap and a
  timeout, inside the sandboxed settings service. The allowed-signers
  line is written by the service from a parsed key only: comment dropped,
  `namespaces="gjallaros-settings"`, never `no-touch-required`. Only then
  is the payload parsed, with the same strict parser.
- `gjallarctl settings export FILE` prints the `ssh-keygen` line to run;
  `gjallarctl settings import FILE` reads `FILE.sig` next to it and fails
  with that line when it is missing. The change shows the key's SHA256
  fingerprint and where the file was made. A file made on another device
  leaves out `scope:"device"` keys, as from a paired device.
- OpenSSH's own code checks the signature, so no new Go dependency
  (`golang.org/x/crypto` has no sshsig).

Security pass (2026-10-20). What went wrong elsewhere, and what this
design does about it:

- Short codes that can be searched for offline (KDE's 8 hex digits, 32-bit
  PGP ids). File exchange has no commit step, so each half is long (30
  digits per key) and slow to compute.
- Names chosen by the other side (KDE review). The hostname is only shown
  as "says it is", never used to decide anything, and text with control
  characters is refused.
- A file or signature made for one purpose used for another. Distinct
  magic lines for settings, pairing and SSH files; the SSH namespace
  `gjallaros-settings` keeps git or other SSH signatures out.
- The file changing between showing the code and adding it. Add takes the
  code the user saw and checks it against the bytes it gets.
- Comparing over the same channel the file came by. The pairing screen
  says to compare on both screens at once; the docs say a code sent by
  message does not count.
- FIDO keys without touch. The service never writes `no-touch-required`.
- A root tool parsing untrusted signatures. `ssh-keygen` runs inside the
  service's sandbox (no network, no capabilities, private /tmp) with fixed
  arguments; the payload is parsed only after it says yes.
- Device settings carried to another device. `scope:"device"` keys never
  come from a paired device.
- Residual, as with settings files: an old file from a paired device stays
  valid until the pairing is removed. It only brings back that person's
  settings as a draft with the tier rules. Logged on the security review
  page.

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
- **Apply.** `gjallarctl system apply` (or the editor button) reads the
  folder as the user and sends it as data to gjallar-settingsd
  (`RequestLocal`), which copies it into root-owned
  `/etc/gjallar/developer/local`: `.nix` files and a short list of asset
  types only, no links, no hidden files, a size cap. Root never reads the
  home folder. The command shows each file's diff against root's copy
  (characters that do not print as escapes) and the new folder hash; the
  change then waits like a security setting, and the password dialog
  lists the files and the same hash. Apply refuses when developer mode
  went off or root's copy changed since the request, swaps the folder
  whole (`RENAME_EXCHANGE` in its own parent, the only part of `/etc` the
  service may write besides the checkout) and rebuilds. The
  root rebuild checks `/etc/gjallar/developer/local` again (root-owned, nothing
  writable by group or others, no links) and stages it as
  `.gjallar-local/` next to the official source; the flake imports it
  from there. No extra flake input, so `flake.lock` stays the official
  one. With developer mode off the folder is not read or staged, and a
  checkout's own `.gjallar-local/` never is.
- **Limits** (internal/localchanges): `README.md`, and below `system/`
  or `home/` files ending in .nix .json .toml .yaml .yml .conf .txt .md
  .png .jpg .jpeg .svg; letters, digits and `. _ + -` in names, nothing
  hidden; at most 4 levels, 256 files, 1 MiB per file, 4 MiB in all.
- **Visible everywhere.** Each generation records the folder's hash
  (the sha256sum manifest, also in `/etc/gjallar/build-source.json`;
  the build stops when the staged copy does not match it); the
  boot entry says "local changes"; `gjallarctl system status` lists the
  files. Later: compare the security-relevant options of the official and
  the local build and list what local code changed (firewall, sshd, ...).
- **Getting out.** If a local module breaks the system, the previous
  generation boots. Recovery and the other extra boot entries never hold
  the local modules (they are disabled in every specialisation); recovery
  offers "Turn off developer mode and rebuild
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

## 15. Snapshots (done)

Plane GJALLAROS-198. Jade decided questions 18 to 22 and 22a on the
questionnaire (2026-10-20 and 2026-10-21): read-only snapshots, root-only,
btrbk-style retention, rollback only through recovery, no secrets and no
`/nix` in snapshots, settings in `user.config.json`, and only new installs
get the layout. Step 1 (installer subvolumes), step 2 (snapshot
units, settings, rebuild snapshots), step 3 (restoring your own
files) and step 4 (rollback in recovery) are built and passed the VM test
(2026-10-21, a new install on a throwaway VM).

Today a fresh install puts everything on the top level of one encrypted
Btrfs (no subvolumes). NixOS generations already roll back the system
itself: the store is read-only and every rebuild is a boot entry. What no
generation brings back is mutable state: `/home`, `/var/lib`, `/etc/gjallar`.
Snapshots are for that.

### How others handle it

- openSUSE keeps `/` on a subvolume and puts paths that must survive a
  rollback (`/home`, `/opt`, `/srv`, parts of `/var`) on their own
  subvolumes, because a snapshot never includes a nested subvolume
  ([openSUSE reference, Snapper](https://doc.opensuse.org/documentation/leap/reference/html/book-reference/cha-snapper.html)).
  The same guide warns not to place snapshots inside the snapshotted
  subvolume, or they can no longer be deleted after a rollback.
- Fedora guides add snapper plus grub-btrfs: booting a snapshot is a
  read-only preview; making it permanent is a separate step from the
  running system
  ([Fedora discussion](https://discussion.fedoraproject.org/t/btrfs-snapshots-in-fedora-the-definitive-step-by-step-easy-and-safe-guide/194483)).
- The NixOS wiki documents btrbk: root timers create read-only snapshots
  into a directory on the top-level volume; restore is manual, and local
  snapshots are no backup when the disk dies
  ([NixOS wiki, Btrbk](https://wiki.nixos.org/wiki/Btrbk)). Snapper is also
  packaged as a NixOS module and runs `snapperd` on D-Bus.

### Security pass

- **Old files stay readable.** A snapshot keeps each file with the
  permissions it had then: a file made private later, or a setuid program
  that was fixed later, is still there in its old form. The Fedora security
  list concluded unprivileged users must not reach snapshot contents
  ([Fedora security list](https://lists.pagure.io/archives/list/security@lists.fedoraproject.org/message/SV63LDF5Y77PGPDFUJ7JMR6WNKSZFD52/));
  snapper denies traversal of `.snapshots` to non-root by default
  ([vfs_snapper](https://man.archlinux.org/man/extra/samba/vfs_snapper.8.en)).
  Design: snapshot directory root-only (0700), never mounted for users,
  and when mounted at all then `nosuid,nodev,noexec`.
- **Deleted secrets stay too.** Btrfs never overwrites data in place, so a
  removed or rotated key lives on in every older snapshot until that
  snapshot is deleted
  ([openSUSE forum](https://forums.opensuse.org/t/secure-remove-from-btrfs/152004)).
  Design: machine secrets (`/var/lib/sbctl`, sealed tokens and keys under
  `/var/lib/gjallar`) stay on `@`, which is never snapshotted. Home
  data cannot be split like that, so a rotation the system performs on a
  user's behalf says which snapshots still hold the old key and offers to
  delete them.
- **Privileged helpers broke in practice.** qSnapper took a config name
  from the caller without checking it (CVE-2026-41046, path traversal to
  root) and reused one polkit result for a different, stronger action
  (CVE-2026-41048, delete permission used for restore). Design: fixed
  snapshot sets with built-in names, nothing taken from the caller as a
  path; each action asks polkit on its own, and deleting snapshots or a
  rollback asks the password every time (§14 rule). Restoring a user's own
  file resolves the path beneath that user's home inside the snapshot,
  follows no symlinks, writes as the user under a new name, and never
  overwrites.
- **Disk filling.** Snapshots of `/nix` would keep every garbage-collected
  store path alive. `/nix` gets its own subvolume and is never
  snapshotted. Below a free-space floor (10% of the disk, at most 10 GiB)
  a run makes no new snapshot and says so in the journal; old ones still
  age out.
- **The NixOS btrbk module is too wide.** In the pinned nixpkgs it runs
  btrbk as its own user with a sudo rule for any `btrfs` command. `btrfs`
  with free arguments can write or delete anywhere, so that user is root
  in all but name. Design: own root oneshot units with no sudo rule,
  `ProtectSystem=strict`, write access only to the snapshot paths, no
  network, `NoNewPrivileges`, and a narrow capability set. A note for
  upstream goes to the human-session queue.
- Snapshots stay inside the LUKS volume, so nothing new is stored in the
  clear. They are not a backup.

### Design (decided)

Only what is listed gets snapshots (question 22a). `/` and `/var/lib` never
do: NixOS generations bring the system back, and the secrets live there.

| Subvolume | Mounted at | Snapshots |
| --- | --- | --- |
| `@` | `/` | never; default subvolume, so a plain mount of the disk (recovery tools) lands here |
| `@home` | `/home` | hourly |
| `@nix` | `/nix` | never |
| `@config` | `/etc/gjallar/system` (the checkout: `user.config.json`, state, hardware) | daily and before every rebuild |
| `@snapshots` | nowhere | holds the snapshots; 0700 root; only the snapshot units mount it, in their own mount namespace, `nosuid,nodev,noexec` |

`/etc/gjallar` itself stays on `@`, because NixOS writes its own links
there. The checkout holds no secrets: its content is in the world-readable
store anyway.

1. New installs get the layout above. The recovery `open-root` command
   mounts `@nix`, `@home` and `@config` below `/mnt` when it finds it,
   never `@snapshots`. The snapshot units turn themselves on only where
   `/home` is `@home`.
2. Snapshots by hardened root timers (btrbk retention rules:
   `snapshot_create onchange`, `preserve_min` plus `preserve`, long ISO
   timestamps, a lock file, a free-space floor before each run).
   `snapshotsEnable` (on), `snapshotsKeepHours` (24) and
   `snapshotsKeepDays` (7) in `user.config.json` set them; a config
   without the keys gets these values.
3. Users restore their own files (`gjallarctl snapshots list`,
   `gjallarctl snapshots restore PATH [--from SNAPSHOT] [--to FOLDER]`);
   only their own home, copied next to the original. This is not the
   settings service but its own bus-started service, gjallar-snapshotd
   (`org.gjallaros.Snapshots1`), so `gjallar-settingsd` keeps an empty
   capability set; the restore service holds only `CAP_SYS_ADMIN` (to
   mount `@snapshots` read-only in its own mount namespace) and
   `CAP_DAC_READ_SEARCH`, and exits after two idle minutes. It checks the
   snapshot name and that the path is in the caller's home before it asks
   the caller's password, every time. It walks to the file beneath that
   home in the snapshot one name at a time with `openat` and
   `O_NOFOLLOW` (no symlinks, no `..`, no other file system), opens only a
   regular file the caller owned, and hands back a read-only descriptor.
   `openat2` would do the walk in one call, but systemd's
   `RestrictSUIDSGID` blocks it, and that filter stays: it stops a
   remount from making setuid files. `gjallarctl` writes the copy as the user under a new name
   (`notes.restored-20261021T030000.txt`) and never overwrites. Folders
   are a later step: a folder descriptor would let `..` out of the home.
4. A rollback of `@home` or `@config` runs only in recovery: the trusted
   recovery boot entry or the recovery partition (with the system at
   `/mnt` from `open-root`). `gjallar-recover snapshots ROOT` lists the
   snapshots, `gjallar-recover snapshot-rollback ROOT NAME` puts one back,
   `gjallar-recover snapshot-drop ROOT NAME` deletes a copy a rollback
   kept. The rollback:
   - takes only a name of the btrbk form from the list and a snapshot that
     is read-only, and asks a typed confirmation (`ROLLBACK-HOME`,
     `ROLLBACK-CONFIG`);
   - makes a new writable snapshot of it; the read-only snapshot is never
     made writable, so it stays as it was;
   - swaps it with the current `@home` or `@config` in one rename
     (`RENAME_EXCHANGE`), so a power cut leaves one of the two in place,
     never none;
   - keeps the replaced one in `@snapshots/replaced` (0700, root only,
     never pruned by btrbk) and makes it read-only as soon as nothing
     mounts it;
   - unmounts the target first; when it is in use (the recovery boot entry
     with a signed-in user), the swap takes effect at the next start and
     says so;
   - lists the nested subvolumes, because a snapshot never holds them and
     they stay with the kept copy;
   - for `@config`, says that the settings are back but the system built
     from them is not, so a `rebuild` follows.
   btrbk keeps finding the older snapshots after a swap (it follows the
   whole parent chain), so they still age out normally.
5. Existing installs keep their layout and get no snapshots until they
   are reinstalled (question 19).

## 16. VPN: exit node down (steps 1 and 2 built)

Draft written 2026-10-20 for Plane GJALLAROS-190: tell the user when the
exit node is down and offer a pause that holds only for this network.
Nothing is built yet; the open choices are questions 23 to 25 on the
questionnaire.

Today `gjallar-vpn-trust.service` (root) decides on every network change.
On an untrusted network a named exit node that is offline stays set, so
Tailscale blocks the internet. The only sign is a `WARN:` line in the
journal and in `gjallarctl vpn status`; the desktop shows nothing, so the
user sees a dead network with no reason. With `exitNode = "auto"` and no
exit node online, the service clears the exit node instead and the
untrusted network carries the traffic in the clear (loophole 24, below).

### How others handle it

- Mullvad's lockdown mode blocks all traffic outside the tunnel while the
  app is disconnected, except the app's own connection attempts; the user
  turns lockdown off in the app to get out
  ([Mullvad app docs, kill switch](https://mintlify.com/mullvad/mullvadvpn-app/security/kill-switch),
  [lockdown command](https://www.mintlify.com/mullvad/mullvadvpn-app/cli/commands/lockdown)).
- Proton VPN's permanent kill switch keeps the device offline unless it is
  connected to a Proton server, also across manual disconnects and
  restarts; leaving it means turning it off in settings
  ([Proton VPN blog](https://protonvpn.com/blog/permanent-kill-switch)).
- Tailscale documents exit nodes and LAN access
  (`--exit-node-allow-lan-access`), but no behaviour for an offline exit
  node; community reports describe exactly our case, internet gone until
  the node is back
  ([Tailscale exit nodes](https://tailscale.com/docs/features/exit-nodes),
  [GL.iNet forum](https://forum.gl-inet.com/t/gl-mt3000-beryl-ax-tailscale-exit-node-breaks-internet-connection/68235)).
- None of the three offers a pause tied to one network: it is all or
  nothing, and "off" stays off until the user turns it back on.

### Security pass

- **The network can cause the prompt.** A hostile network can block the
  tunnel on purpose, so "exit node down" is something an attacker can
  produce at will. Every pause offer is a chance to train people to click
  it. TunnelVision (CVE-2024-3661, DHCP option 121) shows the same goal
  from the other side: get the user off the tunnel. Firewall-style kill
  switches resist it; a user who switches the kill switch off does not
  ([Help Net Security](https://www.helpnetsecurity.com/2024/05/08/tunnelvision-cve-2024-3661/),
  [Zscaler](https://www.zscaler.com/it/blogs/security-research/cve-2024-3661-k-tunnelvision-exposes-vpn-bypass-vulnerability)).
  So the notice is shown once per network join, never repeats, never
  counts down, and the pause asks the password every time (ROADMAP §14
  rule for security changes).
- **Say which side failed.** When the tailnet still reports the exit node
  offline, the node is down (the network is not to blame). When this
  device cannot reach the Tailscale servers at all, the network is
  blocking the tunnel, and the notice says so in plain words. Both texts
  say what a pause exposes: traffic on this network goes out without the
  tunnel.
- **Bound to this network.** An SSID alone can be cloned. The pause keys
  on device, SSID, key management, gateway hardware address and subnet.
  Any change, a disconnect, suspend or reboot ends it. It lives in `/run`
  only and is never written to the override file or `state.nix`.
- **Who may pause.** Only the user at the local seat, through the settings
  service with the password; never over SSH, never from an app. Managed
  (JODS) devices get a policy switch to forbid it later.
- **Visible while it lasts.** The bar shows "VPN paused on NETWORK" until
  it ends; `gjallarctl vpn status` shows the same plus the end time.
- **Logged.** Start, end, user and the network key go to the journal; no
  SSID list or address history is kept beyond that.
- **Loophole 24, auto fails open.** `exitNode = "auto"` with no exit node
  online clears the exit node, so the untrusted network carries the
  traffic in the clear. A named node fails closed in the same situation.
  Proposed fix: `auto` keeps the last exit node set (blocked) and raises
  the same notice; with no exit node ever chosen the firewall blocks
  outbound traffic outside the tunnel and the LAN. This changes what users
  see (no internet instead of plain internet), so it goes with this
  design (question 25).

### Proposed design

1. `gjallar-vpn-trust` writes a `blocked` reason into
   `/run/gjallar/vpn-trust.json`: `exit-node-offline` or
   `tunnel-unreachable`, plus the network key.
2. A user service watches that file and shows one desktop notice per
   network join, with a "Pause VPN on this network" action.
3. The action asks the settings service, which shows old and new value,
   asks the password and writes `/run/gjallar/vpn-pause.json`.
4. `gjallarctl vpn apply` honours the pause only while the network key
   matches and the time limit has not passed (question 24), and clears it
   otherwise.
5. `gjallarctl vpn pause` / `gjallarctl vpn resume` for the terminal, with
   zsh completion; the bar shows the paused state.
6. Loophole 24 fixed as above, once question 25 is answered.

### Step 1: fail closed (built, branch vpn-fail-closed)

Question 25 answered: `auto` blocks, and the pause is the way out. Built
2026-10-21:

- `auto` with no exit node online keeps the current exit node, else sets
  an offline exit node (home router first), else reports "no exit node in
  this tailnet". It never clears the exit node on an untrusted network.
- The verdict carries `blocked`: `exit-node-offline`, `no-exit-node` or
  `tailscale-down`. `gjallarctl vpn status` prints it in plain words.
- Found on the way and closed with it: with Tailscale stopped, a wanted
  exit node did nothing and the network carried the traffic in the clear.
  That is now `tailscale-down`.
- Backstop: while blocked, `gjallar-vpn-trust` loads the nftables table
  `inet gjallar_vpn_block`. It drops traffic that leaves through an uplink
  (every device with an IPv4 or IPv6 default route, not the Tailscale
  device), in the output and forward chains. Let through: tailscaled's own
  packets (its bypass mark), the DHCP client, the local subnets,
  link-local and local multicast. DNS (ports 53 and 853) is dropped even
  to the LAN. The table is swapped in one `nft -f` transaction, so there
  is no moment without rules. The block goes away when the verdict is no
  longer blocked, for example after `gjallarctl vpn exit-node off`.
- VM test `tests/nix/vpn-fail-closed.nix`: headscale, one exit node, a
  router and a client on its own LAN. It checks each blocked reason, that
  the internet is cut and the LAN still answers, that marked packets pass
  and that `exit-node off` lifts the block.

Security pass for step 1:

- **The bypass mark.** Tailscale marks its own control, DERP and
  WireGuard sockets with 0x80000 in the 0xff0000 bits it owns
  ([tailscale.com/util/linuxfw](https://pkg.go.dev/tailscale.com/util/linuxfw),
  [netns_linux.go](https://github.com/tailscale/tailscale/blob/main/net/netns/netns_linux.go)).
  Setting a mark (`SO_MARK`) needs `CAP_NET_ADMIN`, so trusting it trusts
  root only; root can drop the table anyway. Mullvad's split tunnel
  exempts traffic the same way, by mark
  ([Mullvad split tunneling](https://mullvad.net/en/help/split-tunneling-with-linux-advanced)).
- **LAN DNS.** Mullvad blocks DNS to the LAN by default in lockdown mode
  ([Mullvad app docs](https://mintlify.com/mullvad/mullvadvpn-app/security/kill-switch)).
  We do the same: queries to a hostile resolver say what the user looks
  up even when nothing else leaves.
- **DHCP is let through** by port pair, outgoing only. Root-only ports
  (68, 546) on the source side, so a user process cannot use them.
- **Interface names** come from `ip -j route`; anything that is not a
  plain kernel name is refused instead of quoted into the ruleset.
- **Open windows, recorded, not closed by step 1:**
  - Boot: until `gjallar-vpn-trust` runs once, nothing blocks. Closing it
    means a boot-time table before networking; that is step 3 below.
  - A new uplink (USB tethering, a second Wi-Fi) is outside the table
    until the dispatcher reruns the service, a second or so.
  - Signing in to Tailscale again through the browser is blocked on an
    untrusted network; the pause (step 2) is the way out.
- **Default stays off.** With `exitNode = ""` (fresh installs, e2e) the
  block is never loaded.

### Step 2: notice and pause (built, branch vpn-fail-closed)

Questions 23 and 24 answered: the local seat and an administrator's
password, admins too; the pause ends on a network change, before sleep,
on reboot, and after one hour at most. Built 2026-10-21:

- **Notice.** `gjallarctl vpn notice` runs in the session at login and
  each time `/run/gjallar/vpn-trust.json` changes
  (`apps/tailscale/home.nix`). One notice per network join: it says which
  network, what failed, and what a pause gives away, with "Pause VPN on
  this network" and "Keep blocked". It closes by itself when the network
  changes or the block ends. A network without a key (no gateway hardware
  address) gets the notice without the pause button.
- **Bar.** The `gjallar/vpn` noctalia plugin shows a shield while the VPN
  blocks this network or a pause lets it through. A click pauses, or ends
  the pause.
- **Pause.** `gjallarctl vpn pause` asks the settings service
  (`PauseVPN`). It checks the caller sits in a login session, asks the
  polkit action `org.gjallaros.vpn.pause` (an administrator's password,
  every time), checks again that the same network is still blocked, and
  writes `/run/gjallar/vpn-pause/pause.json` for one hour. `gjallarctl
  vpn resume` ends it without a password.
- **Ends.** A transient timer at the end time, the sleep hook
  `gjallar-vpn-pause-sleep`, a network change (the key no longer
  matches), and a reboot (`/run`). `gjallarctl vpn status` prints who
  paused it and until when.
- VM test `tests/nix/vpn-fail-closed.nix` covers the pause: it lets the
  network through, a new address on the uplink ends it, it runs out on
  its own, `resume` and the sleep hook end it, and a pause without the
  settings service is refused. Passed 2026-10-21, with `tailscale-trust`
  still passing.
- Trust runs no longer wait 30 s for a tailscaled that systemd reports
  stopped or failed; that wait held up every decision, a pause among
  them, while Tailscale was down. The block stays in place meanwhile.
- fw13 VM check 2026-10-21 (eaaf444b): the notice and the password
  dialog fit Noctalia (the notice cuts each line after about three rows,
  so the body is three short lines); pause from the notice and from the
  bar, Cancel, "Keep blocked", ending the pause from the bar, a rebuild
  during a pause (the pause survives it) and the `exit-node default`
  undo all passed. Rows in `.vm/installer-e2e/REGRESSION.md` under VPN.

Security pass for step 2:

- **What the pause holds to.** The network key is a hash of the device,
  Wi-Fi name, key management, gateway address, gateway hardware address
  and the subnets. A cloned Wi-Fi name alone does not match; any of these
  changing ends the pause.
- **The pause file.** `gjallar-vpn-trust` uses it only when root owns it,
  nobody else can write it, it names this network, and it lasts at most
  one hour from its start. Anything else counts as no pause, so a broken
  or planted file fails closed.
- **Who may pause.** Only a process in a login session (user units count
  for the display session, as with polkit), and only with an
  administrator's password. The password dialog names the network risk.
  Ending a pause only tightens, so it needs no password.
- **Notice text.** The Wi-Fi name is escaped before it goes into the
  notice body; the bar shows a name with quotes or escapes as "this
  network".
- **Open window, recorded:** while paused, apps on that network reach the
  internet without the tunnel. That is the point of the pause; the dialog
  and the notice say so.

Next: step 3 is the boot-time block.

## 17. Nextcloud: what downloads first on a new machine (done)

Job 173. On a fresh book12 install the client went through about 2100
Music files before `claude/` (lower case sorts last), and ODDC work waited
over half an hour. Jade: everything is a placeholder; content comes down
only for folders the user picks, or for a file the user opens. Folder
names come from the user, never from this repository.

### Today

- New accounts start in virtual-files mode (OpenVFS, patch
  `nextcloud-vfs-default.patch`) with the root pinned online-only. Files
  are placeholders until opened.
- gjallarOS starts the wizard with `--overrideserverurl` and
  `--overridelocaldir`, so it skips the sync options page and creates the
  folder right after login (`AccountWizardController`, client 34.x).
- A folder can already be kept on the device later: the Dolphin menu
  offers "Always available locally" and "Free up local space" for OpenVFS
  (`socketApiPinStateActionsShown()` is true). There is no way to pick
  before the first sync.
- OpenVFS keeps the pin in an xattr on the placeholder
  (`OpenVFS::setPinState`), so a folder can only be pinned once its
  placeholder exists. During discovery a new folder inherits the pin of
  its parent (`ProcessDirectoryJob::computePinState`).
- Open fact (asked ODDC b12, answer 11.2): was book12 in virtual-files
  mode during that sync? If not, the half hour was downloads, and this
  design fixes it. If it was, the first listing itself is slow and needs
  its own look.

### How others handle it

- OneDrive: Files On-Demand is the default; a folder marked "Always keep
  on this device" downloads, and so do new files added to it later
  ([Microsoft](https://support.microsoft.com/office/save-disk-space-with-onedrive-files-on-demand-0e6860d3-d9f3-4971-b321-7092438fb38e)).
- Dropbox: folders not excluded are online-only by default; "available
  offline" downloads one folder. Selective sync is a separate tool that
  hides a folder completely
  ([Dropbox](https://help.dropbox.com/sync/make-files-online-only),
  [UMich](https://teamdynamix.umich.edu/TDClient/30/Portal/KB/ArticleDet?ID=6670)).
- Google Drive for desktop, streaming mode: "Available offline" per file
  or folder, new items in such a folder come down too
  ([Google](https://support.google.com/drive/answer/2375012)).
- Nextcloud: "Choose what to sync" (exclude) works only with virtual
  files off (`Folder::supportsSelectiveSync()`).

All three big ones use placeholders by default and a per-folder "keep on
this device" pin. None asks at first login; gjallarOS adds that one
question because a fresh machine is exactly when the user knows what
they need first.

### Decisions (Jade, 2026-10-20 and 2026-10-21)

- Q27: the picker opens right after the account wizard.
- Q28: new top-level folders made on another device show up as
  placeholders, like everything else.
- Closing the picker without a pick: everything stays a placeholder.
- The pick is not saved anywhere outside this device's client. A
  reinstall or a second machine starts with an empty picker. (Considered:
  `user.config.json`, which reaches the world-readable Nix store, build
  agents and the recovery image; a 0600 file in home, which needs an
  unencrypted export to travel; a hidden file in the user's own Nextcloud,
  which would expose names of end-to-end encrypted folders.)

### Security pass

- Folder names are private data. The pick stays in the client's journal
  database inside the sync folder, which already holds every name. The
  patch logs counts, never names.
- A pin downloads every file in that folder, also files others add later
  to a shared folder inside it. The picker says so in one line. A new
  share lands where the server's share folder setting puts it; inside a
  pinned folder it downloads too, anywhere else it is a placeholder.
- Disk space: upstream already stops downloads before the disk is full
  (`criticalFreeSpaceLimit`, `freeSpaceLimit` in
  `owncloudpropagator.cpp`). The picker shows the size of the pick next to
  the free space, so the user sees it before saying OK.
- A hostile or broken server can send odd folder names (control
  characters, right-to-left marks). The worst outcome is a wrong folder
  downloaded, not data leaving the device; names are shown the way Qt
  shows file names everywhere else.
- No server change, nothing excluded: the server copy is never touched,
  and unpinning later ("Free up local space") keeps files with local
  changes: OpenVFS refuses to turn a file changed since the last sync
  back into a placeholder (`OpenVFS::createPlaceholder`).

### Design

1. Client patch, wizard side: when the wizard created the folder without
   showing sync options (the gjallarOS override case) and the folder is
   OpenVFS, the folder starts paused and the picker opens: the account's
   folder tree with sizes, nothing ticked, title "Keep on this device",
   buttons OK and "Not now". The tree is the existing
   `SelectiveSyncWidget`; ticked folders become the pick.
2. The pick goes into the journal database as its own list type, then
   the folder un-pauses. "Not now" writes nothing.
3. Client patch, engine side: during discovery a folder on that list
   counts as pinned "always local" even before its placeholder exists, so
   its files download in the first sync instead of becoming
   placeholders. After each finished sync the client writes the real pin
   on every listed folder that now exists and drops it from the list. A
   folder that is gone from the server after a full sync is dropped too.
4. Later changes stay with the Dolphin menu ("Always available locally",
   "Free up local space"); README says where.
5. Tests: client patch check in `tests/nix/nextcloud-client-patches.nix`;
   e2e-fw13 against a SYNTHETIC test account and folder tree: pick one of
   three folders, check that only it is hydrated and pinned after the
   first sync, a new top-level folder arrives as a placeholder, "Not now"
   leaves all placeholders, and a quit during the first sync keeps the
   pick.

### Built (2026-10-21)

All passed on the fw13 VM against a local SYNTHETIC test server.

- Picker and placeholders: 8ee44c2e. The pick survives a kill during the
  first sync and applies after the restart.
- Q42: openvfs unmounts when the client exits; a mount left from before
  the client started is a setup error (713c504b).
- A second start with a bad option no longer exits the running client
  (5630fd4c). A plain launch opens the settings window (713395d8).
- Q43: a crashed client restarts after 10 s, five starts in five minutes
  at most. Quit stays quit (27c1646f).
- Q44: the settings window floats at its own size instead of being cut
  off, and the folder list is titled "Sync folders" (74f1fb25).

## 18. Time sync: authenticated, the same on every network

Written 2026-10-20 for Plane GJALLAROS-200 ("time sync follows the VPN").
Jade answered questions 30 to 33 on 2026-10-20; step 1 is built, steps 2
and 3 are next.

Until step 1, `system/default.nix` turns on chrony with
`server 192.168.8.1 iburst prefer` plus the NixOS default pool
(`0-3.nixos.pool.ntp.org`). All of it is plain NTP: nothing proves who
answered. The router line is the same on every install and every network,
and 192.168.8.0/24 is a common router default, so a laptop on a foreign
network with its own 192.168.8.1 prefers that box's time (loophole 25).
The clock matters here: USB and Thunderbolt approvals lapse by time
(section on `lastUsed`, above), TLS checks certificate dates, and logs and
snapshots are ordered by it.

### How others handle it

- Ubuntu 25.10 and later ship chrony with NTS on by default: four
  `ntp.ubuntu.com` NTS pools, plus a bootstrap server
  `ntp-bootstrap.ubuntu.com` whose certificate comes from Ubuntu's own CA
  so a machine with a badly wrong clock can still start. Time from DHCP is
  commented out. If NTS-KE (TCP 4460) is blocked, there is no sync
  ([Ubuntu chrony client docs](https://ubuntu.com/server/docs/how-to/networking/chrony-client/),
  [Phoronix](https://www.phoronix.com/news/Ubuntu-25.10-Chrony),
  [OMG! Ubuntu](https://www.omgubuntu.co.uk/2025/06/ubuntu-chrony-nts-default-25-10)).
- Fedora ships chrony with the plain pool; NTS is opt-in. Fedora Magazine
  and the Fedora NTS change page show NTS servers such as
  `time.cloudflare.com` and Netnod's `nts.netnod.se`
  ([Fedora Magazine](https://fedoramagazine.org/secure-ntp-with-nts/),
  [Fedora change page](https://fedoraproject.org/wiki/Changes/NetworkTimeSecurity)).
- GrapheneOS does not use NTP at all, NTS included; it reads the time from
  an HTTPS response header of its own server
  ([GrapheneOS forum](https://discuss.grapheneos.org/d/2609-ntphtpdate-and-other-servers/24)).
- Hardening guides for chrony say the same three things: NTS sources only,
  more than one operator, no DHCP-supplied servers
  ([dev.to guide](https://dev.to/lyraalishaikh/stop-trusting-plain-ntp-practical-network-time-security-with-chrony-on-linux-2g5a)).

### Security pass

- **Plain NTP can be moved by anyone on the path.** A café network, a
  hotel or anything between the laptop and the pool can answer with any
  time. With the router line, the attacker does not even need to be on the
  path: owning a 192.168.8.1 on the local network is enough.
- **NTS stops forged time, not blocked or delayed time.** chrony's own
  manual: with authenticated sources an attacker can drop or delay
  packets, up to the `maxdelay` and `maxdistance` limits, but cannot change
  the timestamps; the damage is a limited slew or step. It also says the
  unauthenticated sources must go, or they become the way in
  (chrony.conf(5), `authselectmode`).
- **One operator should not set the clock alone.** `minsources 2` with
  sources from different operators means a single wrong or hostile server
  cannot move the clock; with one source left, chrony holds the clock
  instead (chrony.conf(5), `minsources`).
- **A wrong clock breaks NTS itself.** NTS-KE is TLS, so a clock far off
  rejects every certificate. chrony offers `nocerttimecheck N`, which skips
  the date check for the first N updates; its manual calls this a last
  resort with "important security implications" because an expired, leaked
  key would be accepted while the check is off. Ubuntu avoids it with its
  own bootstrap CA, which we do not have. Most laptops keep a working RTC,
  so the window is small, but it is a real trade (question 33).
- **Blocked port means no time.** A network that blocks TCP 4460 stops NTS.
  On an untrusted network the exit node carries the traffic, so the block
  only bites on trusted networks or with the VPN off. Falling back to plain
  NTP would hand the attacker the clock again, so the fallback is "keep
  the current clock and say so" (question 32).
- **Privacy.** Each NTS operator sees the laptop's address and that it runs
  NTS. On untrusted networks that is the exit node's address.
- **What the VPN adds.** The tunnel is already authenticated: a router
  reached at its tailnet address over WireGuard is a source nobody on the
  local network can forge. That is the one place where "follows the VPN"
  helps, if the router serves time there (question 31).

### Decisions (questions 30 to 33)

- **Q30:** four operators: Cloudflare, Netnod, PTB, time.nl.
- **Q31:** the router is a fallback only when no NTS server works, and
  only on a proven home network (its tailnet address if the router serves
  time there).
- **Q32:** plain NTP fallback only on a proven home or site network, with
  small corrections only; elsewhere keep the clock and show "time not
  confirmed since ...".
- **Q33:** no own bootstrap server and no manual setting. Research further
  (JODS as a time server later, maybe); if the first-update exception
  stays, harden it.

### Research for question 33 (2026-10-21)

- All four operators answered NTS-KE on TCP 4460 with a certificate that
  verified: `time.cloudflare.com`, `nts.netnod.se`, `ptbtime1.ptb.de` to
  `ptbtime4.ptb.de` and `ntspool.time.nl`. Netnod, PTB and time.nl use
  90-day Let's Encrypt certificates, Cloudflare a one-year certificate.
  A clock that is behind fails a certificate only when it is earlier than
  the certificate's start date, so with renewals every 60 days or so the
  clock has to be about two months or more behind to break NTS.
- Three things keep the clock close without any exception: the RTC, which
  works on most laptops; `chronyd -s`, which starts from the time chronyd
  last stopped when that is later than the RTC; and the NixOS clock floor
  (`time-epoch`, the release date of the build). NTS cookies saved in
  `ntsdumpdir` let chronyd skip the TLS key exchange after a restart, so a
  clock that starts wrong can still be corrected with saved cookies.
- The cases left are a dead RTC on a machine that was off for months, or a
  machine that comes back from storage. There the only tools are
  `nocerttimecheck` or a second time protocol.
- Roughtime (RFC 10049, Experimental) gives signed time without
  certificates: the client knows the server's long-term key and needs no
  date check. time.nl runs a pilot at `rough.time.nl` port 2002. There is
  no Roughtime client in nixpkgs and chrony does not speak it. It fits
  "JODS as a time server later": a JODS server could answer Roughtime with
  a key that ships with the install. Not built now.

### Design

**Step 1, built 2026-10-21 (`system/services/time.nix`):**

- The router line and the NixOS pool are gone from every install. This
  closes loophole 25.
- One NTS source per operator: `time.cloudflare.com`, `nts.netnod.se`,
  `ptbtime1.ptb.de`, and `ntspool.time.nl` as a pool with
  `maxsources 1`. With `minsources 2`, one operator alone never moves the
  clock.
- `authselectmode require`: chrony selects authenticated sources only.
- No servers from DHCP: the NixOS chrony dispatcher only switches sources
  on and off line.
- `chronyd -s` and `enableNTS` (which sets `ntsdumpdir`) keep a clock that
  starts wrong within reach of NTS.
- `gjallarctl system status` shows `Time: confirmed by NTS at <time>`, or
  `Time: not confirmed since <time>` when no source updated the clock in
  the last two hours or chronyd lost sync. Read from `chronyc tracking`,
  which every user may run.
- Nix check `checks.time` reads the chrony.conf the module generates and
  runs `chronyd -p` on it.

**Step 2, the hardened first-update exception (Q33):**

- Off by default; strict NTS is what runs.
- A helper looks at chronyd shortly after start. Only when no NTS source
  got past the key exchange and the reason is the certificate date, it
  writes a config under `/run` with `nocerttimecheck 1` and restarts
  chronyd once.
- `minsources 2` stays: two operators have to agree on the first update,
  so one bad key alone cannot set the clock.
- The exception is used up by that first update (N = 1), lives only in
  `/run`, is logged, and `gjallarctl system status` says the time was set
  with the date check off until a normal NTS update confirms it.

**Step 3, router fallback on a proven home or site network (Q31, Q32):**

- Only when no NTS source works and the VPN verdict is home or site (a
  router proven by its signed answer, see `internal/nettrust`).
- The router's tailnet address when it serves NTP there; otherwise its
  proven local address.
- chronyd restarts with a fallback config that allows that one
  unauthenticated source, with small corrections only: no steps, and a
  low limit on how fast and how far the clock may move.
- When the network stops being home or site, or an NTS source works
  again, the strict config comes back.
- Status says the time came from the router, not from NTS.

**Later:** the time line in the settings app, and a notice when the time
has not been confirmed for a long time.

**Tests:** step 1 has the Nix check and the e2e-fw13 run. Steps 2 and 3
need NixOS VM tests: a plain NTP server on the LAN that the client must
ignore, only one source answering (the clock must not move), and a clock
months behind with the exception.
