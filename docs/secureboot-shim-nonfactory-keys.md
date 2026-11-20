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
   N (default) = today's behavior (install finishes with the error).
   Unattended/answers file: no answer = N. Own-keys path keeps the strict
   check, no question.
2. **ODDC-known models skip the question** — when ODDC recognizes the model
   and its security facts describe the factory PK (fingerprint list, or
   "PKDefault absent on this firmware"), a matching PK counts as factory and
   no question is shown. ODDC owns the facts (handoff to ODDC agent: field
   name, format, T480s entry from this report). Unknown or mismatch → question 1.

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

## Code pointers

- `internal/installer/secureboot/ownership.go` — `Inspect()` now reports
  `PKDefaultPresent`/`PKSHA256`; record gains `ownerUnknownAccepted`
  (`RecordOwnershipUnknownAccepted`)
- `internal/installer/secureboot/shimflow.go` — `PrepareShim(ctx, out,
  unknownOwnerAccepted, factoryPKMatch)` + `shimForeignProceed`
- `internal/installer/oddc/` — `factoryPKFingerprints` + `pkDefaultAbsent`
  under `vendor.policy.secureBoot`, `MatchesFactoryPK`
- `internal/installer/app/security_flow.go` — `plane388Classify` +
  `unknownPKOwnerQuestion`; called from `app.go` after the Secure Boot
  decision, before disk work; `PrepareShim` call passes both flags
- `internal/installer/app/install_report.go` — `saveFailureToBootStick`
  writes error text + redacted log to the stick's GJLOGS on any failure

## Implementation status (2026-11-20)

- Unit tests green: `shimflow_test.go`, `oddc/manifest_internal_test.go`,
  `app/plane388_test.go`; `go build ./...`, `go test ./cmd/... ./internal/...`
  pass. Plane 388 comment posted.
- Open: e2e VM rerun (`vm.sh up … --secureboot --volatile-defaults` = no
  PKDefault, T480s-like; answer y then N to the new question); ODDC agent adds
  the T480s entry with `pkDefaultAbsent`/fingerprints.
