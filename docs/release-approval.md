# Approving an installer release

Every `v*` tag builds an installer release, but the release key signs it
only after a person approves the `sign` job. This page is that approval,
step by step: when to do it, what to check first, where to click, and what
to do when something is wrong. The whole release flow and its one-time
setup are in [Maintainers → Installer release](maintainers.md#installer-release).

The workflow that runs is the one in the tagged commit, `sign` included.
The approval is the only thing between that file and the signing key, so
the checks below are the point of the step, not a formality.

## Who can approve

- A member of the `installer-sign` environment's required reviewers.
- Not the person who pushed the tag ("Prevent self-review"). The release
  bot (`release-botGjallar`) pushes tags, so a maintainer can approve.

## Set up the shell

All commands below use `gh` and run from any directory; none of them
needs the checkout. Set the tag you want to approve once. Replace
`v2026.10.67` with the real tag name:

```sh
R=JadeOpenServices/gjallarOS
NEW=v2026.10.67
RUN=$(gh run list --repo $R --workflow installer-iso.yml --branch $NEW --json databaseId --jq '.[0].databaseId')
LAST=$(gh release list --repo $R --exclude-drafts --limit 20 --json tagName --jq 'map(select(.tagName|test("^v[0-9]{4}\\.[0-9]+\\.[0-9]+$")))[0].tagName // empty')
echo "run $RUN, last release ${LAST:-none}"
```

`LAST` is the newest published installer release. It is empty before the
first one; the `nix-cache` release is not an installer release and is
skipped.

## 1. Wait until it is ready

Approve when all three are true:

```sh
gh api repos/$R/actions/runs/$RUN/jobs --jq '.jobs[] | .name + " " + .status + " " + (.conclusion//"")'
gh release view $NEW --repo $R --json isDraft,assets --jq '.isDraft, (.assets[].name)'
gh api repos/JadeOpenServices/gjallarOS-statements/contents/$NEW/gjallar-builder --jq '.[].name'
```

| Command | Ready when it prints |
| --- | --- |
| Jobs | `build completed success` and `sign waiting` |
| Draft | `true`, then six files: the `.iso`, `gjlogs.img`, `RELEASE`, `make-stick.sh`, `make-stick.ps1`, `SHA256SUMS` |
| Builder statement | `SHA256SUMS` and `SHA256SUMS.sshsig` |

`build` takes 1 to 6 hours. The gjallar builder looks for new tags once
an hour and then builds for about as long. Until its statement is there,
the third command fails with `Not Found (HTTP 404)`.

You can approve before the statement is there. `sign` then stops with
`No builder statement for vYYYY.MM.N in JadeOpenServices/gjallarOS-statements yet`
before it touches the key, and `publish` is skipped. When the statement
arrives, open the run and click **Re-run jobs** → **Re-run failed jobs**,
or:

```sh
gh run rerun $RUN --repo $R --failed
```

The re-run asks for the approval again; do step 3 once more. It reuses
the files `build` made. An approval you did not give waits up to 30 days.

## 2. Check what you are about to sign

### The tag came from the release bot

```sh
gh api repos/$R/actions/runs/$RUN --jq '"pushed by: " + .triggering_actor.login + "\nrun commit: " + .head_sha'
gh api repos/$R/commits/$NEW --jq '"tag commit: " + .sha + "\n" + (.commit.message|split("\n")[0])'
```

- `pushed by:` is `release-botGjallar`.
- `run commit` and `tag commit` are the same hash.
- The commit message is the release commit you asked the bot to tag.

### The commit is on a channel branch

```sh
for b in dev staging stable; do echo "$b: $(gh api repos/$R/compare/$b...$NEW --jq .status)"; done
```

At least one line says `behind` or `identical`: the tag commit is on that
branch. `ahead` or `diverged` on all three means it is on none of them.
Then do not approve; `build` should have stopped already.

### The workflow is one you read

If `LAST` is set, show what changed in the workflow since the last
release:

```sh
gh api "repos/$R/compare/$LAST...$NEW" --jq '.files[] | select(.filename==".github/workflows/installer-iso.yml") | .patch'
```

No output: the workflow is unchanged, and you can go on. Output: read
every changed line before you approve.

If `LAST` is empty (the first release), or the change is large, read the
whole file as it is in the tag:

```sh
gh api "repos/$R/contents/.github/workflows/installer-iso.yml?ref=$NEW" -H 'Accept: application/vnd.github.raw' | less
```

What to look for in the `sign` job:

- It has no `uses:` lines and no checkout. It runs only the shell in the
  file, never code from the repository.
- `secrets.RELEASE_SIGNING_KEY` shows up once, in the step "Sign
  SHA256SUMS". There the key goes into a temporary file only its user can
  read, `openssl` signs with it, and the file is deleted right after.
- Before that step, "Check the builder statement" checks the builder's
  signature with `BUILDER_SSH_KEY` and compares its `SHA256SUMS` with
  CI's, byte for byte.
- Its permissions are `contents: read`.

A quick count of the first two:

```sh
gh api "repos/$R/contents/.github/workflows/installer-iso.yml?ref=$NEW" -H 'Accept: application/vnd.github.raw' | grep -n 'uses:\|secrets\.'
```

For v2026.10.67 this prints three lines: `actions/checkout` and
`cachix/install-nix-action` in `build`, each pinned to a full commit hash,
and the `RELEASE_SIGNING_KEY` line in `sign`. A new `uses:` line, a
`uses:` without a full hash, or any other `secrets.` line is a change you
must understand first.

## 3. Approve

In the browser:

1. Open the run. Its address is printed by
   `echo https://github.com/$R/actions/runs/$RUN`. Without that: the
   repository → **Actions** → in the left list **installer release** →
   the run whose label is the tag name (`v2026.10.67`), titled with the
   release commit's message.
2. At the top of the run, a box says the workflow is waiting for review.
   Click **Review deployments**.
3. Tick **installer-sign**.
4. Write a short comment, for example `v2026.10.67 checked`.
5. Click **Approve and deploy**.

GitHub also sends an e-mail to the reviewers when the run starts to wait;
its link opens the same run.

To see from the shell that the approval is open to you:

```sh
gh api repos/$R/actions/runs/$RUN/pending_deployments --jq '.[] | .environment.name + " can_approve=" + (.current_user_can_approve|tostring)'
```

It prints `installer-sign can_approve=true`. `false` means you pushed the
tag yourself, or you are not one of the environment's reviewers.

## 4. Check the result

`sign` takes about a minute, `publish` a few more.

```sh
gh api repos/$R/actions/runs/$RUN/jobs --jq '.jobs[] | .name + " " + .status + " " + (.conclusion//"")'
gh release view $NEW --repo $R --json isDraft,assets --jq '.isDraft, (.assets[].name)'
gh api repos/$R/releases/latest --jq '"Latest: " + .tag_name'
```

- The jobs print `sign completed success` and `publish completed
  success`.
- The release prints `false` (no longer a draft), then seven files: the
  six above plus `SHA256SUMS.sig`.
- `Latest:` names the release new sticks get.

The new tag is Latest only if `stable` has the same tree as the tag. A `dev` or
`staging` release is published but not Latest; see
[Latest and stable](maintainers.md#latest-and-stable).

If a job failed, its message is in the table
[When a job fails](maintainers.md#when-a-job-fails).

## When a check fails: reject

Do not approve a run you have doubts about. Reject it:

1. In the run: **Review deployments** → tick **installer-sign** → write
   why → **Reject**.
2. Delete the draft, so nobody mistakes it for a release:

   ```sh
   gh release delete $NEW --repo $R --yes
   ```

3. If the tag itself is wrong, delete it too. The tag rules let only the
   release bot delete tags, so this runs from the checkout with the bot's
   token, like the tag push in
   [Per release](maintainers.md#per-release) step 4:

   ```sh
   git -c credential.helper= -c 'credential.helper=!f() { echo username=release-botGjallar; printf "password="; secret-tool lookup service github.com user gjallaros-release-bot; echo; }; f' push origin :refs/tags/$NEW
   ```

4. Fix the cause, and tag the fixed commit with a new number. The old
   number keeps its builder statement in gjallarOS-statements, so do not
   reuse it.

If you are unsure whether the tag came from the bot at all, also check
the bot's token: who could use it, and whether it should be renewed
([Release bot](maintainers.md#3a-release-bot)).

## Wrong commands people try

- `git diff vLAST vNEXT -- .github/workflows/installer-iso.yml` in the home
  directory fails with "Limiting comparison with pathspecs is only
  supported if both paths are directories": git runs outside a checkout,
  and `vLAST`/`vNEXT` are placeholders, not tags. Use the `gh api` commands
  above; they need no checkout.
- In the checkout, `git diff` works only with real tag names that exist
  locally. The `gh api` forms avoid both problems.
