# Plane GJALLAROS-388 — shim path refuses firmware with no PKDefault (friend's T480s, L161)

Date: 2026-11-20. Inspection + implementation done 2026-11-20 (see bottom).
Sources: Plane issue `05d733ac` (GJALLAROS-388, "Installer: Secure Boot shim
path refuses firmware with no PKDefault (T480s, L161)", weight 30), wiki book
gjallarOS page 720 "Security review (private)" item **L161**, code in
`internal/installer/secureboot/`.

## Problem

- 2026-11-19, friend's ThinkPad T480s (20L7S2BC00): BIOS locked, Secure Boot
  on, Setup Mode off, no ODDC model, **no `PKDefault` efivar** (older
  firmware does not expose it).
- Install stopped in `PrepareShim` (`internal/installer/secureboot/shimflow.go`
  `StateForeignManaged`, called late from `app.go:1341`): "the firmware holds
  keys that are neither its factory keys nor gjallarOS keys".
- Root cause: `ownership.go:204-209` and `:235-239` count the PK as factory
  only when `defaultPresent && samePlatformKey(pk, pkDefault)`. No PKDefault
  efivar → `defaultPresent` false → falls through to `StateForeignManaged`
  (`ownership.go:251`). c9623478 fixed owner-GUID matching but still requires
  PKDefault to exist.
- The refusal is wrong for the shim path: shim+MOK never changes PK/KEK/db,
  it only adds the gjallarOS key to MOK; shim having started already proves db
  trusts the Microsoft UEFI CA. Replacing keys was never possible on the T480s
  (needs Setup Mode; BIOS locked).
- Also observed: the refusal came late (after disk work) and the stick log
  had no error text (only the ODDC report).

## Jade's decision 2026-11-19 (build both)

1. **y/N question when the PK owner is unknown** — shim path, SB on,
   ShimStarted, state foreign: ask in the question phase *before any disk
   work*. Text: the firmware does not say whether its SB owner key is the
   factory key; gjallarOS only adds its own key to shim's list and leaves the
   firmware keys alone; if this computer belongs to a company or someone else
   set its keys, say no. y = record answer (ownership stage `pending-MOK`,
   `owner unknown-accepted`) and proceed with MOK as on factory firmware.
   N (default) = stop before the disk is touched and wait for ODDC support
   of the model (Jade 2026-11-22: "continue with our shim or wait for oddc").
   Unattended/answers file: no answer = N. Own-keys path keeps the strict
   check, no question.
2. **ODDC-known models** (Plane 391, ODDC-128) — ODDC lists the model's
   factory PK sums as a fact and names the proof in an ownership policy
   (see "ODDC contract" below). Then, instead of question 1:
   - active PK on the list, ownership `verified`: no question.
   - active PK on the list, ownership `experimental`: one short question
     (Jade 2026-11-22): "ODDC confirms this is the factory key, but
     installing through the gjallarOS shim has not been tested on this model
     yet. The firmware keys are not touched. Continue with the gjallarOS shim
     now? [y/N]". Unattended stops.
   - list known, active PK not on it: the same y/N as question 1 with a
     stronger text (Jade 2026-11-22): the key is NOT one of the factory keys
     ODDC knows for this model. Default no, Secure Boot must be on,
     unattended stops.
   - no usable ODDC entry: question 1.

Also in the ticket:

- (a) Classify early: run the ownership read in **preflight**, so a refusal
  comes before the disk is touched (today `PrepareShim` runs late, app.go:1341).
- (b) On any install failure, write the error text and the secret-filtered
  install log to **GJLOGS** (the T480s stick only had the ODDC report).
- (c) Loophole pass: the answer must come from the root-owned installer flow,
  not a user-writable file; planted-answers-file test; a foreign PK that
  later changes is caught by the next ownership check.
- (d) Tests: no PKDefault + y, + N, + unattended; ODDC match/mismatch;
  own-keys still refuses.

## Review fixes 2026-11-22

Review of the first cut (c933ec8d, 34f259e4) found gaps; fixed:

- **ODDC match only by fingerprint.** `pkDefaultAbsent` is dropped. A
  model-wide "no PKDefault" flag says nothing about who set the PK, so a
  company- or attacker-set PK on such a model would have skipped the
  question. Only a listed factory PK fingerprint counts now.
- **ODDC path recorded no PK binding.** After reboot `Inspect` saw a valid
  record, no PKDefault and no binding → `StateInconsistent`, and
  `ContinueShim` refused. Both the ODDC path and the y path now write
  `firmwarePKSHA256` into the record; `Inspect` keeps the record pending
  only while the active PK hashes to that value (`shimRecordBindsPK`).
- **Accepted answer is bound to the PK.** The y answer carries the PK hash
  it was given for (`ShimOwnerFacts.AcceptedUnknownPKSHA256`); `PrepareShim`
  re-inspects and proceeds only if the PK is still that one. A PK swapped
  between question and key step, or after install, is refused or makes the
  record inconsistent.
- **ODDC path checks shim too.** Secure Boot on + shim not started is
  refused on both paths.
- **Refusals before disk work.** `plane388Classify` now gets
  `ShimStarted()` and refuses SB off (unknown owner), shim not started and an
  unreadable PK before anything is written. `PrepareShim` repeats the checks.
- **Stick error file redacted.** `saveFailureToBootStick` wrote the raw
  error; it now uses `installreport.ErrorText` with this run's known values,
  like the report.

## ODDC contract (Plane 391, ODDC-128)

Both paths sit at the top level of the resolved device data
(`/etc/oddc/resolved.json`).

- Fact: `hardware.firmware.secureBoot.factoryPKFingerprints`, a non-empty
  list of lowercase hex SHA-256 sums (64 characters, no duplicates) of the
  DER certificate in the first entry of the factory PK signature list.
- Policy: `policy.secureBootOwnership = {"proof":
  "pk-sha256-in-factory-list", "status": "experimental" | "verified"}`. It
  points at the fact and never copies the list.
- The sbctl recipe (`vendor.policy.secureBoot`) is separate; a model can
  have the ownership proof without a recipe (T480s). Own gjallarOS keys
  still need the recipe.
- Fail closed: unknown proof, missing or unknown status, or a missing,
  empty or malformed list make the whole entry count as absent. The
  installer prints why and asks question 1.
- Catalog only: a project or host overlay can override keyed paths, so the
  installer reads the ownership entry only when every path under both
  entries comes from the ODDC catalog. An overlay that touches them makes
  the entry count as absent (loophole 163).

The first version of this fix read the list from
`vendor.policy.secureBoot.factoryPKFingerprints`; that reader is gone.

## Recovery partition on the shim path (Jade 2026-11-22, R51)

The recovery partition is signed with the same gjallarOS db key as the
system and is started from the systemd-boot menu, so shim+MOK check it the
same way. Only starting it through shim is untested (Plane 293). The
installer used to drop it on the shim path; it now asks
`shimRecoveryPartitionQuestion` (default no) in `collectRecovery`. y offers
the full recovery choice, no keeps the boot entry only. Managed installs,
answers files and unattended runs are not asked and stay without it. A yes
risks only an untested boot path, not trust: an unsigned or foreign-signed
partition still fails the shim check.

## Code pointers

- `internal/installer/secureboot/ownership.go` — `Inspect()` reports
  `PKDefaultPresent`/`PKSHA256`; record gains `firmwarePKSHA256` (the PK the
  shim path was settled for) and `ownerUnknownAccepted` (audit);
  `shimRecordBindsPK`
- `internal/installer/secureboot/shimflow.go` — `PrepareShim(ctx, out,
  ShimOwnerFacts)` + `shimForeignProceed`
- `internal/installer/oddc/ownership.go` — `ResolveSecureBootOwnership`,
  `SecureBootOwnership.MatchesFactoryPK`, `Verified`
- `internal/installer/app/security_flow.go` — `plane388Classify` +
  `unknownPKOwnerQuestion`, `notFactoryPKQuestion`, `untestedShimQuestion`; called from `app.go` after the Secure Boot
  decision, before disk work; returns the `ShimOwnerFacts` that `PrepareShim`
  gets
- `internal/installer/app/install_report.go` — `saveFailureToBootStick`
  writes the redacted error text + redacted log to the stick's GJLOGS on any
  failure

## Implementation status (2026-11-22)

- Unit tests green: `secureboot/shimflow_test.go` (incl. PK binding),
  `oddc/ownership_test.go`, `app/plane388_test.go`.
- e2e VM run 2026-11-22 (OVMF without PKDefault, T480s-like):
  - y: PASS. Question before disk work, shim + MOK install, Secure Boot boot
    chain shim -> systemd-boot -> lanzastub, TPM2+PIN unlock.
  - N: PASS. Stop with "stopped before anything was written", target disk
    has no partition table or signature afterwards.
- Open: failure save to GJLOGS on a maker stick (a dd'ed ISO has no GJLOGS;
  the save returns silently when no stick drive is found); ODDC data for
  the T480s (waits on the T480s PK read), then the ODDC pin bump.
