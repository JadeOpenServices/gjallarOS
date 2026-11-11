#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'GJALLAR_RECOVERY_ERROR=%s\n' "$*" >&2
  return 1
}

[ "$(id -u)" -eq 0 ] || fail 'executor requires root'

contract="${1:-}"
artifact="${2:-}"

[ -n "$contract" ] || fail 'contract path required'
[ -n "$artifact" ] || fail 'artifact path required'
[ -s "$contract" ] || fail 'contract does not exist or is empty'
[ -s "$artifact" ] || fail 'artifact does not exist or is empty'

# This binary is intentionally recovery-only.
[ -e /etc/gjallar/recovery ] || [ -e /etc/gjallar/recovery-image ] || \
  fail 'not running inside a gjallarOS recovery environment'

for command in jq sha256sum lsblk realpath awk grep; do
  command -v "$command" >/dev/null || fail "required command missing: $command"
done

jq -e '
  .version == 1 and
  (.command.payload.schema_version == 1) and
  (
    .command.payload.operation == "reinstall" or
    .command.payload.operation == "factory-wipe"
  ) and
  (.command.payload.job_id | type == "string" and length > 0) and
  (.command.payload.registration_id | type == "string" and length > 0) and
  (.command.payload.nonce | test("^[0-9a-fA-F]{64}$")) and
  (.command.payload.artifact_digests.os_image.sha256 |
    test("^[0-9a-fA-F]{64}$")) and
  (.command.payload.artifact_digests.target_disk.gpt_disk_guid |
    test("^[0-9a-fA-F-]{36}$")) and
  (.command.payload.artifact_digests.target_disk.recovery_partition_uuid |
    test("^[0-9a-fA-F-]{36}$"))
' "$contract" >/dev/null || fail 'contract schema is invalid'

operation="$(jq -r '.command.payload.operation' "$contract")"
job_id="$(jq -r '.command.payload.job_id' "$contract")"

expected_sha="$(jq -r \
  '.command.payload.artifact_digests.os_image.sha256' \
  "$contract" | tr '[:upper:]' '[:lower:]')"

actual_sha="$(sha256sum "$artifact" | cut -d ' ' -f 1)"
[ "$actual_sha" = "$expected_sha" ] || fail 'OS artifact digest mismatch'

expected_disk_guid="$(jq -r \
  '.command.payload.artifact_digests.target_disk.gpt_disk_guid' \
  "$contract" | tr '[:upper:]' '[:lower:]')"

expected_recovery_uuid="$(jq -r \
  '.command.payload.artifact_digests.target_disk.recovery_partition_uuid' \
  "$contract" | tr '[:upper:]' '[:lower:]')"

expected_wwn="$(jq -r \
  '.command.payload.artifact_digests.target_disk.wwn // empty' \
  "$contract")"

expected_serial="$(jq -r \
  '.command.payload.artifact_digests.target_disk.serial // empty' \
  "$contract")"

# Find the recovery partition the way the Boot Loader Specification does:
# the ESP the loader ran from (LoaderDevicePartUUID), its disk, and the one
# XBOOTLDR partition on that disk. A label search across all disks could pick
# up a second disk that carries a copy of the layout.
loader_partuuid_var=/sys/firmware/efi/efivars/LoaderDevicePartUUID-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
esp_type='c12a7328-f81f-11d2-ba4b-00a0c93ec93b'
xbootldr_type='bc13c2ff-59e6-4262-a352-b275fd6f7172'
uuid_pattern='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
identity_pattern='^[A-Za-z0-9._:-]{4,256}$'

[ -r "$loader_partuuid_var" ] || \
  fail 'boot loader did not report its partition (LoaderDevicePartUUID)'
# efivars start with 4 attribute bytes; the value is UTF-16.
loader_uuid="$(tail -c +5 "$loader_partuuid_var" | tr -d '\000' | tr '[:upper:]' '[:lower:]')"
printf '%s' "$loader_uuid" | grep -Eq "$uuid_pattern" || \
  fail 'LoaderDevicePartUUID is not a UUID'

mapfile -t esps < <(
  lsblk -pnro PATH,PARTUUID |
    awk -v uuid="$loader_uuid" 'tolower($2) == uuid { print $1 }'
)
[ "${#esps[@]}" -eq 1 ] || fail 'boot partition is missing or not unique'
esp="${esps[0]}"
[ "$(lsblk -dnro PARTTYPE "$esp" | tr '[:upper:]' '[:lower:]')" = "$esp_type" ] || \
  fail 'boot partition is not an ESP'

parent_name="$(lsblk -dnro PKNAME "$esp")"
[ -n "$parent_name" ] || fail 'cannot resolve the boot disk'
target_disk="$(realpath "/dev/$parent_name")"
[ "$(lsblk -dnro TYPE "$target_disk")" = disk ] || \
  fail 'target is not a whole disk'

mapfile -t recovery_partitions < <(
  lsblk -pnro PATH,PARTTYPE "$target_disk" |
    awk -v type="$xbootldr_type" 'tolower($2) == type { print $1 }'
)
[ "${#recovery_partitions[@]}" -eq 1 ] || \
  fail 'expected exactly one XBOOTLDR partition on the boot disk'
recovery_partition="${recovery_partitions[0]}"

[ "$(lsblk -dnro TYPE "$recovery_partition")" = part ] &&
  [ "$(lsblk -dnro PARTLABEL "$recovery_partition")" = GJALLAR-RECOVERY ] &&
  [ "$(lsblk -dnro LABEL "$recovery_partition")" = GJALLAR-REC ] || \
  fail 'XBOOTLDR partition is not the gjallarOS recovery partition'

actual_recovery_uuid="$(lsblk -dnro PARTUUID "$recovery_partition" | tr '[:upper:]' '[:lower:]')"
[ "$actual_recovery_uuid" = "$expected_recovery_uuid" ] || \
  fail 'recovery partition UUID mismatch'

actual_disk_guid="$(lsblk -dnro PTUUID "$target_disk" | tr '[:upper:]' '[:lower:]')"
[ "$actual_disk_guid" = "$expected_disk_guid" ] || \
  fail 'GPT disk GUID mismatch'

# Every identity the signed command names must match, and it must name at
# least one. Values that are not plain identifiers count as absent, the same
# rule the agent uses when it reports the disk.
actual_wwn="$(lsblk -dnro WWN "$target_disk" | xargs)"
actual_serial="$(lsblk -dnro SERIAL "$target_disk" | xargs)"
printf '%s' "$actual_wwn" | grep -Eq "$identity_pattern" || actual_wwn=''
printf '%s' "$actual_serial" | grep -Eq "$identity_pattern" || actual_serial=''

[ -n "$expected_wwn" ] || [ -n "$expected_serial" ] || \
  fail 'signed contract contains no disk identity'
[ -z "$expected_wwn" ] || [ "$actual_wwn" = "$expected_wwn" ] || \
  fail 'disk WWN mismatch'
[ -z "$expected_serial" ] || [ "$actual_serial" = "$expected_serial" ] || \
  fail 'disk serial mismatch'

# Do not allow an artifact to change which physical disk the signed
# authorization refers to.
printf '%s\n' \
  "GJALLAR_RECOVERY_OPERATION=$operation" \
  "GJALLAR_RECOVERY_JOB_ID=$job_id" \
  "GJALLAR_RECOVERY_TARGET_DISK=$target_disk" \
  "GJALLAR_RECOVERY_PARTITION=$recovery_partition" \
  "GJALLAR_RECOVERY_ARTIFACT_SHA256=$actual_sha" \
  'GJALLAR_RECOVERY_VALIDATION=passed'

# IMPORTANT:
# The repository currently has no canonical target-disk install primitive.
# Never replace this fail-closed boundary with inline sgdisk/mkfs/nixos-install.
#
# The next implementation must live in gjallarOS and accept ONLY the already
# validated target disk/artifact/operation from this function.
platform_executor='/run/current-system/sw/bin/gjallar-recovery-platform-install'

[ -x "$platform_executor" ] || \
  fail 'validated contract, but canonical gjallarOS platform installer is not implemented'

"$platform_executor" \
  --operation "$operation" \
  --target-disk "$target_disk" \
  --recovery-partition "$recovery_partition" \
  --artifact "$artifact" \
  --artifact-sha256 "$actual_sha" \
  --job-id "$job_id"
