# Two-stage install: design

Status: **design only, nothing built yet** (Plane 385, checkpoint CP-T0, 2026-11-26).
This page is the plan of record for splitting the install into a short
offline step on the stick and a setup step on the installed system. It
collects the decisions taken so far (questionnaire Q263 to Q292), the parts
of today's installer we keep, the security design and the build order. The
README and the install guide keep describing today's install until the new
one is built (CP-T7).

## Why

Today the stick asks every question, downloads the system from the release
cache and builds it, all before the first boot of the new system. That has
four costs:

- The stick needs a network link for most of an hour, and a lost link costs
  the whole run (Plane 431)
- Everything that needs the finished system runs on the stick instead:
  security chip (TPM) enrolment, the recovery key, Secure Boot checks.
  Some machines only accept the chip enrolment once the system they protect
  has booted (the T480s case, Plane 442)
- A mistake late in the questions means starting again from the first one
- The stick has to carry the whole installer look in a terminal

Windows 11, Fedora and Ubuntu Core all split the install the same way: a
small image goes onto the disk first, and the questions that need a running
system come after the first boot. We follow that pattern.

## Words used on this page

| Word | Meaning |
| --- | --- |
| Stage 1 | The stick part. Asks only disk-level questions and writes a small signed base system to the disk. Works offline |
| Base system | A minimal gjallarOS that can boot, unlock the disk and run stage 2. It is a release output with its own signature (`gjallar-base`) |
| Stage 2 | The setup on the installed base system after the first reboot. A graphical app in a locked kiosk session |
| Setup marker | A root-owned file on the encrypted root that says stage 2 is not finished. Without it, stage 2 never starts |
| Setup helper | The one root service that does every root step for stage 2. The setup app talks to it over a local socket |
| Full install | The existing offline "fat" install from a stick that carries the whole release cache (Plane 418). Stays as its own path |

## The flow

```
stick (terminal)            first reboot             installed disk (graphical)
----------------            ------------             --------------------------
keyboard, language          firmware -> shim         Secure Boot check
disk, size check            MokManager:              network (Wi-Fi or cable)
disk passphrase             enrol gjallarOS key      managed or standalone
Secure Boot notice                                   device name, account
write base system                                    recovery key, saved and proved
queue gjallarOS key                                  security chip unlock
write setup marker                                   update to the full system
                                                     first-run guide, then login
```

### Stage 1 on the stick

The stick stays a terminal program. That keeps the stick image small and
easy to look after (Q273).

Questions, and only these (Q269):

1. Keyboard and language
2. Target disk, with the size check before anything is erased (40 GiB
   minimum, Plane 362)
3. Disk passphrase. This is the only secret typed on the stick, and it is
   needed at every boot until stage 2 replaces it
4. Secure Boot notice (see "Secure Boot" below)

Steps after the questions:

1. Check the stick against the newest signed release (as today,
   `stickrelease`)
2. Partition, format, open the encrypted root (as today, `freshdiskplan`,
   `diskcrypto`)
3. Copy the base system from the stick. The base system is a signed
   release output stored on the stick. Every path is checked against the
   release key before it lands on the disk (Q264). No network is used
4. Install the boot chain: shim, the signed boot loader and kernel
5. Queue the gjallarOS signing key for MokManager, and also put the public
   gjallarOS certificate on the boot partition (Q285, see "Missed MokManager
   step")
6. Write the setup marker and the non-secret stage 1 answers (keyboard,
   language, Secure Boot choice) on the encrypted root
7. Ask to reboot

Nothing secret is carried across the reboot (Q276). The disk passphrase is
typed again at the unlock screen. No temporary partition is made.

The stick menu keeps "More options" for the full install maker (Q271) and
for IT staff who want the terminal path (see "Terminal path").

### First reboot and MokManager

The firmware starts shim. Shim finds the queued key and opens MokManager,
where the person confirms the gjallarOS key with the one-time password the
stick showed. This is the same step as today.

#### Missed MokManager step

If the person misses or cancels MokManager (Q285, Q287):

- Shim refuses the unknown boot loader and offers MokManager again. The
  person can pick "Enroll key from disk" (the MokManager menu entry) and choose the public certificate
  on the boot partition
- Stage 2 compares the enrolled keys with the certificate fingerprint that
  stage 1 wrote on the encrypted root. An unknown key shows a red warning
  and goes on the delete list (Plane 444)
- A stick menu entry "Queue the key again" reads the certificate from the
  boot partition, without unlocking the disk. We only build it if a VM
  test shows that MokManager does not open by itself

### Stage 2 on the installed system

#### Session

Stage 2 is a graphical Qt/QML app in a kiosk session (Q273):

- It runs as a system user `gjallar-setup` with no password and no shell.
  Its home is a fresh tmpfs for each boot
- The kiosk compositor shows one window and nothing else. No terminal, no
  file manager, no "open with", no other apps
- No text consoles, no VT switch, no SysRq, no Ctrl+Alt+Del action (Q265)
- The normal login screen does not start while the setup marker exists
- The app itself has no root rights. Every root step goes through the
  setup helper (see "Setup helper")

Choice of kiosk compositor (cage or KWin in kiosk mode) is made at CP-T4.
The pick needs: no VT switch, no way to start other programs, the KDE look
of the final system, and the least code we have to write and keep. We will check
both against those four points and write the result into this page.

#### Screens, in order

##### 1. Secure Boot check

Reads the live state: Secure Boot on, Setup Mode off, the gjallarOS key
enrolled. If Secure Boot is off, the screen says what it protects, and the
person can go on with a typed confirmation (Q267). The check runs again at
every boot.

##### 2. Network

Wi-Fi list or cable. Stage 2 waits here until there is a link (Q266). A
cable link carries on by itself, even from the Wi-Fi screen. "Turn off,
continue later" keeps all progress.

##### 3. Managed or standalone

Standalone is the default for private machines and is never a hidden
choice. Managed is never preselected (Q274). Until the Genbu management
service is ready, this step is a stub: it checks the device contract
version and shows the consent screen, and does nothing more (Q275, Genbu
answer of 2026-11-25). No routes or payloads are invented.

##### 4. Device name and local account

Standalone: the person types them. Managed (later): Genbu supplies them and
the person confirms.

##### 5. Recovery key

Made here on the installed system, never on the stick (Q276 to Q281).
Format: the systemd recovery key, 64 lower-case modhex characters (256
bits), readable on any keyboard layout. The screen explains how strong it
is. All four ways to save it are offered:

- write it down and type it back
- QR code to a phone: bare key text, no link; the screen says to scan it
  with a password manager or an offline camera app, never an online lens
- a file on a separate USB drive: never the system disk, never the install
  log stick, with a warning if it is the installer stick
- Genbu escrow: managed only, stub until Genbu is ready

Every way ends with typing the key back (Q278). The Secure Boot recovery
archive and its passphrase are saved in the same step (Q279).

##### 6. Security chip unlock

Runs only with Secure Boot enforced (Q268). Enrols the TPM2 unlock against
the booted system. If the chip refuses (the T480s case), the disk keeps its
passphrase and the screen says so. This step never breaks the boot. The
passphrase from stage 1 is removed only after a later boot has unlocked
with the chip (today's `gjallarctl luks setup` rule).

##### 7. Update to the full system

Checks the release channel (default: latest stable; for managed machines
later, the channel from the Genbu enrolment profile), downloads and
switches to the full gjallarOS with the answers from steps 3 and 4.
Progress is shown in plain words, with a "Details" view of the real log
for IT staff.

##### 8. Finish

The helper creates the account, removes the setup marker as the very last
step, and restarts into the normal login. The first-run guide starts at
the first login (keys, where to change them, updates).

#### Admin and user split

For company and OEM setups (Q274, Windows Autopilot pattern): an admin can
do steps 1 to 3 and then pick "Shut down, user finishes later" or "Run
user setup now". The user part then starts at step 2 (Wi-Fi again if
needed). The full managed flow is designed with the Genbu team at their
phase D (JODS-697). We do not build user sign-in until then.

### Setup helper

One root service, `gjallar-setup-helper`, does every root step of stage 2.
It is the only root code the setup app can reach.

- It runs only while the setup marker exists, and stops for good when the
  marker is removed
- It listens on a socket in `/run` that only the `gjallar-setup` user can
  open. The helper checks the caller's user ID on every connection from
  the kernel's peer credentials. Nothing the caller sends counts for this
- It knows a fixed list of requests (see below). Each request has a typed
  body with a size cap. Unknown requests and unknown fields are refused
- The caller never sends a path, a command, a device node or a file name
  that the helper opens. The helper works out every path itself. A USB
  drive for the recovery key is picked from the helper's own list of
  removable drives by its list entry, never by a path from the app
- Secrets (account password, recovery key type-back, disk passphrase for
  the first chip enrolment) travel only in the request body. They are never
  in program arguments, environment variables, logs or files
- One request at a time. A request that changes state is written to the
  helper's own state file before it starts, so a power loss leaves a known
  state (see "Resume and failure")

Requests (first list, refined at CP-T4):

| Request | Does |
| --- | --- |
| `status` | Setup progress, Secure Boot state, enrolled-key check |
| `network-list`, `network-connect` | Wi-Fi scan and connect through NetworkManager |
| `management-choice` | Records standalone or managed (stub) |
| `identity` | Device name, account name, account password |
| `recovery-key-create`, `recovery-key-confirm` | Makes the key, proves the type-back |
| `recovery-usb-list`, `recovery-usb-save` | Lists eligible drives, writes the key file |
| `chip-enroll` | TPM2 enrolment on the booted system |
| `update-check`, `update-run` | Channel check, download and switch |
| `finish`, `power-off` | Ends setup, or keeps progress and turns off |

The helper gets the full root loophole pass before it is merged
(see "Security").

### Resume and failure

- Every step is written to the helper's state file on the encrypted root
  before it runs and marked done after. After a power loss, stage 2 starts
  at the first step that is not done
- A step that crashed the boot three times stops being retried by itself,
  the same limit as today's install resume (`resume.MaxAttempts`). The
  screen then offers "Try again" and "Turn off" only
- The base system always boots, whatever stage 2 did. The update in step 7
  is a normal NixOS switch: the base system stays in the boot menu until
  setup has finished
- "Turn off, continue later" is on every screen that waits for the person

### Unattended installs

An answers file can preset stage 1 and the non-secret parts of stage 2.
Secure Boot follows the answers file on models ODDC has not verified (Q292):

- answers ask for Secure Boot: stage 1 stops before the disk is touched,
  until ODDC marks the model verified,
- answers say no Secure Boot: install goes on,
- answers say nothing: install goes on without Secure Boot, with the
  notice

The recovery key has to be proved by a person (Q278). An unattended run
therefore stops at the recovery key step, unless Genbu escrow is used on
a managed machine. Open point O3 below.

### Terminal path

IT staff and people who prefer a terminal get the same stage 2 in a
terminal under "Advanced" on the stick (Q273). It is the same helper with
a terminal front end, so there is still only one install path. Wi-Fi works there too.

### Secure Boot: shim only

Stage 1 offers Secure Boot only through shim (Q286, Q291). The code for
installing gjallarOS firmware keys (own keys instead of the factory keys) is removed in the same release. Machines
that already use their own keys keep booting.

| Firmware state | Stage 1 does |
| --- | --- |
| Factory keys, Microsoft 3rd-party UEFI CA in db | Shim install, MokManager at first boot |
| 3rd-party CA switched off (Secured-core PCs) | Stops before the disk with "Switch on Allow Microsoft 3rd Party UEFI CA in the BIOS, then start again", or goes on without Secure Boot (Q288) |
| Setup Mode (no keys) | Stops with "Restore factory keys in the BIOS, then start again", or goes on without Secure Boot (Q290) |
| Only the old 2011 Microsoft CA | Installs; Settings shows a note with the firmware update steps (Q289) |
| Secure Boot off | Guide with the model's steps from ODDC when known, else generic; "Install anyway" needs a typed confirmation (Q267) |

The check that the firmware db holds the shim signer runs before the disk
is touched, using the known Microsoft certificate hashes, instead of after
the shim build as today (Q292). The installer never writes firmware
variables and never asks for a BIOS password (Q283).

For managed machines, every planned PCR 7 change (shim update, MOK change,
Microsoft 2023 CA rollover) is announced to Genbu, so its attestation can
take the new value (Genbu answer, JODS-704).

## What we reuse

| Today | Becomes |
| --- | --- |
| `stickrelease` release floor check | Same, in stage 1 |
| `freshdiskplan`, `diskcrypto`, size check | Same, in stage 1 |
| `secureboot` shim flow, key queue (`requeue`) | Stage 1, own-key parts removed |
| `resume` claim and attempt counter | Pattern for the stage 2 state file; shared code instead of a copy |
| `gjallarctl luks setup` (recovery key, passphrase removal) | Called by the helper for steps 5 and 6 |
| Install log and anchors (`installlog`) | Both stages write to it; stage 2 continues the same log |
| Answers files (`installedanswers`) | Split into stage 1 and stage 2 parts |

What goes: the network download on the stick for the normal install, the
TPM enrolment on the stick, the own-key Secure Boot path. The full install
(Plane 418) keeps its own offline copy.

## For humans

Every part must be possible to run, check and fix by hand (Q270, Q272):

- `gjallarctl setup status` shows the stage 2 state and the next step
- `gjallarctl setup resume` runs the next step from a root shell, for
  maintainers on a dev build
- A maintainer runbook in `docs/maintainers.md` and on the wiki
- A user troubleshooting section in the README
- One VM test page per scenario on the wiki test guide (book 1332), with
  the exact commands and screenshots

## Security

Research: Plane 384 (2026-11-23). Sources are listed at the end.

### What we protect

- The disk key material: disk passphrase, recovery key, chip enrolment
- The account: no admin account may come into being that the owner did not
  make
- The boot chain: the person must notice when Secure Boot is off
- The finished system: stage 2 must not leave a door behind

### Who we plan against

| Who | Can | Design answer |
| --- | --- | --- |
| Someone at the keyboard during stage 2 | Use every screen and key | Kiosk with no shell or VT; helper accepts only fixed requests |
| Someone with the machine between the stages | Boot other media, change the boot partition | Setup marker and stage 1 data on the encrypted root; boot partition holds only the public certificate; Secure Boot rechecked in stage 2 |
| The network during stage 2 | Read and change traffic | Release cache signatures checked as today; no secret goes over the network |
| A flaw in the setup app (Qt, QML, image parsing) | Run code as `gjallar-setup` | That user has no root rights, no shell, no sudo; the helper treats every request as hostile |
| A later local user | Read leftovers | Setup user, socket and helper are gone after finish; no secret in logs or files |

### Rules from the research

1. The setup trigger lives on the encrypted root and is root-owned. Stage 2
   starts only when the marker is there, never because "no user exists"
   (the GNOME initial setup bug, CVE-2020-16125)
2. No shell escape in setup (Windows Shift+F10 in OOBE opens a command
   prompt as SYSTEM)
3. Secrets travel only through the socket body, never in logs, arguments
   or files (Ubuntu Subiquity CVE-2020-11932; cloud-init CVE-2023-1786)
4. The system must boot after any failed setup step
5. Resume after power loss
6. Users must notice when Secure Boot is off. Windows does not tell them
7. The helper checks the caller with kernel peer credentials and does not
   depend on a message bus race (polkit CVE-2021-3560)
8. No set-user-ID program is added (polkit `pkexec` CVE-2021-4034)

### Loophole pass on the helper (before merge)

Every root step of the helper gets the standing root check:

- Does root open a path the setup user can change? Paths are derived by
  the helper; data comes through the socket
- Links at the source, the target and every parent folder, also under
  `/mnt` and on the chosen USB drive
- FIFOs and device nodes where a file is expected
- Size caps on every request and every file the helper reads
- Races between check and use (the USB drive, the state file)
- Who reads each root-written file later

Each finding gets a test that plants the link or FIFO, and an entry in the
loophole log. Details of the findings stay on the private security page.

### Open security points

- Shell in stage 2 for debugging: none now (Q265). Rediscuss later, dev
  build only (Plane 419)
- The kiosk compositor pick (CP-T4) needs its own check for ways to start
  other programs

## Open points

| # | Point | Owner |
| --- | --- | --- |
| O1 | User sign-in without mail access for managed machines | Genbu phase D (JODS-697) |
| O2 | Does the device or the server make the local admin password (managed) | Genbu (JODS-698) |
| O3 | Unattended runs and the recovery key type-back | Jade (questionnaire) |
| O4 | Which Genbu maintainability practices we adopt | Jade (questionnaire) |
| O5 | cage or KWin kiosk | CP-T4 check, written here |
| O6 | "Queue the key again" on the stick: needed or not | VM test at CP-T8 |

## Build order

| Checkpoint | Work |
| --- | --- |
| CP-T0 | This page, on the wiki too |
| CP-T1 | `gjallar-base` output and the signed base copy on the stick; size check |
| CP-T2 | Stage 1 split and the setup marker |
| CP-T3 | Secure Boot off guide and the typed override; shim-only stops (Q288, Q290) |
| CP-T4 | Stage 2 kiosk session and setup helper, with the loophole pass on the helper |
| CP-T5 | Stage 2 screens, Genbu stub, update and switch, resume, first-run guide |
| CP-T6 | Security chip enrolment in stage 2 |
| CP-T7 | Docs for humans: README, install guide, maintainers, wiki |
| CP-T8 | VM scenarios, one test guide page each |
| CP-T9 | Loophole pass over both stages, then release |

## Sources

- Microsoft: Windows Setup configuration passes, OOBE, and BitLocker
  "waiting for activation"
- Fedora `gnome-initial-setup`; CVE-2020-16125 (gdm3 starts initial setup
  again when the accounts service does not answer)
- Ubuntu Core first boot; CVE-2020-11932 (Subiquity wrote the disk
  passphrase into the install logs)
- cloud-init CVE-2023-1786 (sensitive data in logs)
- polkit CVE-2021-3560 (D-Bus caller check race) and CVE-2021-4034
  (`pkexec`)
- ParticleOS and nixos-anywhere (small image first, system after)
- Genbu answers 2026-11-25 (JODS-697, JODS-698, JODS-704) and the ODDC
  answer on shim only (Plane 385 comments)
