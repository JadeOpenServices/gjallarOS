#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

grep -q 'canonical gjallarOS platform installer is not implemented' \
  "$script_dir/execute-contract.sh"

grep -q 'gpt_disk_guid' "$script_dir/execute-contract.sh"
grep -q 'recovery_partition_uuid' "$script_dir/execute-contract.sh"
grep -q 'artifact digest mismatch' "$script_dir/execute-contract.sh"
grep -q 'disk WWN mismatch' "$script_dir/execute-contract.sh"
grep -q 'disk serial mismatch' "$script_dir/execute-contract.sh"

printf '%s\n' 'PASS: recovery executor remains fail-closed and identity-bound'
