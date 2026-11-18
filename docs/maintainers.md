# Maintainers

Notes for people who work on gjallarOS itself: trying changes, the
release cache and the installer release.

## Trying a gjallarctl change

To try a `gjallarctl` change before rebuilding, run it from the checkout with
`go run ./cmd/gjallarctl rebuild` (or any other command). It uses the sudo
helpers of the installed system, so this works only on a machine that already
runs gjallarOS. If you need the package itself, build it with
`nix build "git+file://$PWD#gjallarctl"`. A `path:` flake reference copies the
whole directory into the Nix store, untracked files and VM disks included.

## Release cache

gjallarOS patches a few packages (Noctalia, its greeter, the Nextcloud client
with OpenVFS, OpenCode, Hyprland, Ghostty, Dolphin and the KDE apps that
come with it, and others), so cache.nixos.org does not have them and every
machine compiled them itself. For each `v*` release tag,
`.github/workflows/cache.yml` builds them (flake package `prebuilt`), signs
them and uploads them as files of the `nix-cache` release. Those files are a
plain Nix binary cache. Each tag adds to it, so a machine on any channel
downloads a package whenever it matches one built for any tag. Anything
missing still builds locally. The workflow itself downloads from the same
cache, so a tag builds only the packages that changed since the tags before
it.

`system/release` holds the name of the last release tag. The commit that gets
a new tag sets it to that tag first; the workflow stops when the two differ.
Commits after a tag keep the last release name until the next one.

`deployment/binary-cache.json` names the cache and its public key:

```json
{
  "url": "https://github.com/JadeOpenServices/gjallarOS/releases/download/nix-cache",
  "publicKey": "gjallaros-cache-1:..."
}
```

Machines add it to `nix.conf`, and `gjallarctl rebuild` and the installer
pass it on the command line too, so the first install uses it as well. Nix
only accepts files signed with that key.

The workflow reads the signing key (secret `NIX_SIGNING_KEY`) only through the
`release-cache` environment, which should allow `v*` tags only and require a
reviewer. Create the key on your machine; it never needs network:

```sh
nix key generate-secret --key-name gjallaros-cache-1 > cache.sec
nix key convert-secret-to-public < cache.sec
```

Paste `cache.sec` into the secret, put the public key into
`deployment/binary-cache.json`, then delete `cache.sec` or keep it offline.
The key signs cache files and nothing else. Forks keep
`deployment/binary-cache.json` empty or point it at their own cache.

## Installer release

Each `v*` tag also becomes a GitHub release with the installer stick and
the two stick makers. `.github/workflows/installer-iso.yml` builds it,
waits until a second machine (the gjallar builder) built the same files,
has it signed with the release key, and publishes it. The makers that
people run with `curl ... | bash` or `irm ... | iex` download from
`releases/latest`, so "Latest" decides what new sticks get.

### What a release holds

| File | What it is |
| --- | --- |
| `gjallarOS-installer-vYYYY.MM.N.iso` | The stick image, at most 2,000,000,000 bytes |
| `gjlogs.img` | The GJLOGS partition the makers add behind the image, 32 MiB |
| `RELEASE` | Three lines: `version=`, `date=` (commit day, UTC), `floor=` |
| `make-stick.sh` | Linux maker with the release public key, version and floor filled in |
| `make-stick.ps1` | Windows maker, the same |
| `SHA256SUMS` | SHA-256 of the five files above |
| `SHA256SUMS.sig` | RSA-PSS signature of `SHA256SUMS` with the release key |

The makers check `SHA256SUMS.sig` with the key built into them, then each
file against `SHA256SUMS`, before they write anything. Nix builds all of
it as the flake package `gjallar-release`; that build also refuses a
README that does not show this key's fingerprint and these makers'
SHA-256 (see [Per release](#per-release)).

### How a release goes through

```text
git push origin vYYYY.MM.N
  │
  ├─ build     nix build gjallar-release, checks, uploads a draft release
  │            (no signature yet; nobody outside the repo sees a draft)
  │
  │            meanwhile the gjallar builder builds the same tag on its own
  │            and pushes a signed SHA256SUMS to gjallarOS-statements
  │
  ├─ sign      waits for your approval (environment installer-sign), then:
  │            builder statement signed by the builder key? same bytes as
  │            CI's SHA256SUMS? -> sign SHA256SUMS with RELEASE_SIGNING_KEY
  │
  └─ publish   draft still holds exactly the built files? signature checks?
               -> upload SHA256SUMS.sig, publish, mark Latest if stable has
               this tree, delete old releases

git push origin <commit>:stable
  └─ promote   marks the published release with the same tree as Latest
```

The jobs split the rights on purpose. `build` runs the tagged code and can
write releases, but never sees the signing key. `sign` sees the key, but
runs no code from the repository and cannot write anything. `publish` can
write, but only checks and moves files.

### One-time setup

#### 1. Release key

Make the key on your own machine. It needs no network. RSA, at least 3072
bits, exponent 65537 (openssl's default). Make it in a private folder
outside the checkout, so it can never be committed. Run these from the
checkout:

```sh
mkdir -m 700 ~/release-key
(umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out ~/release-key/release.key)
openssl pkey -in ~/release-key/release.key -pubout -out deployment/release-key.pem
git add deployment/release-key.pem
```

- Commit `deployment/release-key.pem`. Without it the flake has no
  `gjallar-release` package and the stick refuses to install. The flake
  is read as `git+file`, which sees only files git knows: until the
  `git add`, every build says the flake "does not provide attribute
  ... gjallar-release-makers".
- Put the key into the secret `RELEASE_SIGNING_KEY` of the
  `installer-sign` environment straight from the file, so it never goes
  through the clipboard (after step 2). `gh` prints
  `✓ Set Actions secret RELEASE_SIGNING_KEY for JadeOpenServices/gjallarOS`:

  ```sh
  gh secret set RELEASE_SIGNING_KEY --env installer-sign --repo JadeOpenServices/gjallarOS < ~/release-key/release.key
  ```
- Keep one encrypted copy, then delete the plain file. Without a copy,
  losing the secret means a new key
  ([Changing the release key](#changing-the-release-key)). We keep it
  in Nextcloud, encrypted with a passphrase that lives in the password
  manager. The second command decrypts the copy, compares it with the
  original and deletes the original only when they match (it prints
  `MATCH`). `--no-symkey-cache` keeps gpg-agent from remembering the
  passphrase:

  ```sh
  mkdir -p ~/Nextcloud/Backup/gjallarOS
  gpg --symmetric --no-symkey-cache --cipher-algo AES256 --s2k-digest-algo SHA512 --s2k-count 65011712 -o ~/Nextcloud/Backup/gjallarOS/release.key.gpg ~/release-key/release.key
  gpg --no-symkey-cache -d ~/Nextcloud/Backup/gjallarOS/release.key.gpg | cmp - ~/release-key/release.key && echo MATCH && rm -r ~/release-key
  ```

  An encrypted stick in a drawer works the same way. Only decrypt the
  copy to set the secret again; it never goes anywhere else.

The key signs `SHA256SUMS` and nothing else. The `sign` job checks that the
secret belongs to `deployment/release-key.pem` of the tag, so a key change
needs both in place before the tag.

#### 2. Environment installer-sign

Settings → Environments → New environment → `installer-sign`:

- Deployment branches and tags: "Selected branches and tags", add the tag
  rule `v*`. No branches.
- Required reviewers: you. Tick "Prevent self-review" and untick "Allow
  administrators to bypass configured protection rules".
- Environment secret `RELEASE_SIGNING_KEY`: the private key from step 1.
- Environment variable `BUILDER_SSH_KEY`: the builder's public SSH key as
  one `allowed_signers` line:

  ```text
  gjallar-builder namespaces="gjallaros-release" ssh-ed25519 AAAA...
  ```

  The builder writes its public key to
  `/var/lib/gjallar-builder/statement-key.pub`. Copy the `ssh-ed25519
  AAAA...` part and put the two words in front.

"Prevent self-review" means the person who pushed the tag cannot approve
the signing. That is why a bot pushes the tags: you approve what the bot
tagged. With the bot's token in the wrong hands, someone could tag,
but not sign.

#### 3. Tag and branch rules

First a team for the bot: Organization → Teams → New team
`gjallaros-release`, visibility "Visible" (a secret team cannot be a
ruleset bypass actor), repository `gjallarOS` with the role "Write". No
other repository, no other members. You manage it as organization owner
without being in it; being in it would let your own login push tags.

Then Settings → Rules → Rulesets in `gjallarOS`, three rulesets:

| Ruleset | Targets | Rules | Bypass |
|---|---|---|---|
| `release tags v*` | tags `v*` | Restrict creations, updates, deletions; block force pushes | team `gjallaros-release` only |
| `channel branches: no force push or delete` | branches `dev`, `staging`, `stable` | Restrict deletions; block force pushes | nobody |
| `channel branches: only org owners push` | branches `dev`, `staging`, `stable` | Restrict updates | Organization admin |

- Only the bot makes, moves or deletes release tags. You do not tag
  yourself: "Prevent self-review" would then stop you from approving
  your own tag. Fixing a wrong tag goes through the bot too.
- The bot has write access but cannot push the channel branches, so its
  token alone cannot put new code on `dev` and tag it.
- Nobody force pushes or deletes a channel, you included. Promotions are
  merge commits (`git push origin <commit>:staging` after a merge), so
  every push is a fast-forward. `build` refuses a tag whose commit is on
  none of the channels, so these branches decide which code can become
  a release.
- An organization owner can still edit a ruleset. That is on purpose for
  emergencies; each change shows in the organization's audit log.

Also turn on Organization → Settings → Authentication security →
"Require two-factor authentication" once your login and the bot have 2FA.

#### 3a. Release bot

The bot is a separate GitHub login (a machine account), and you control it.
GitHub allows one per person, used only for automation, and the person who
made it is responsible for it.

1. Make a second GitHub account for the bot. Open a private browser
   window (so your own login stays signed in elsewhere) and go to
   `https://github.com/signup`. GitHub wants an email address that no
   other GitHub account uses, so not the one of your own login. Use a
   second address or an alias that lands in your inbox, for example
   `release-bot@` on your own domain. Pick a username such as
   `gjallaros-release-bot`, a password from your password manager, and
   turn on 2FA right after (an authenticator app; keep its recovery codes
   with the password). Our bot's login is `release-botGjallar`; the
   commands below use it. A fork puts its own bot's login there.
2. Invite it to the organization as a member and add it to the team
   `gjallaros-release`. Accept the invite on the bot login.
3. On the bot login: Settings → Developer settings → Personal access
   tokens → Fine-grained tokens → Generate new token.
   - Token name `gjallarOS tags`, Resource owner `JadeOpenServices`,
     expiration at most one year.
   - Repository access: "Only select repositories" → `gjallarOS`.
   - Permissions start empty. Click "Add permissions", tick "Contents",
     then switch its access from "Read-only" to "Read and write".
     "Metadata: Read-only" is added by itself and is required. Add
     nothing else.
   - Generate token.
   - Approve it on your own login: Organization → Settings → Personal
     access tokens → Pending requests → the bot's request → Approve. The
     organization requires approval for fine-grained tokens. Until then
     the token works for nothing, and a tag push fails with `403` and
     `Permission to JadeOpenServices/gjallarOS.git denied to
     release-botGjallar`.
4. Store the token in the keyring of the machine that pushes the tags.
   Paste it into the prompt, so it is on no command line and in no file.
   `service github.com user gjallaros-release-bot` only names the
   keyring entry; the push below looks it up by those words:

   ```sh
   secret-tool store --label "gjallarOS release bot" service github.com user gjallaros-release-bot
   ```

   gjallarOS ships `secret-tool` (libsecret). On a machine without it,
   put `nix shell nixpkgs#libsecret -c` in front of the command.

To stop the bot at once: revoke the token, or remove the account from the
team. Before the token expires, make a new one, have it approved and
store it the same way (`secret-tool store` replaces the old entry).

#### 4. Statements repository and builder

`JadeOpenServices/gjallarOS-statements` is a public repository that holds
one folder per tag:

```text
vYYYY.MM.N/gjallar-builder/SHA256SUMS
vYYYY.MM.N/gjallar-builder/SHA256SUMS.sshsig
```

The gjallar builder (`deployment/builder`) builds each new tag by itself,
writes the `SHA256SUMS` of its own build, signs it with
`ssh-keygen -Y sign -n gjallaros-release` and pushes both files straight to
`main`. This follows Bitcoin Core's guix.sigs: anyone can compare two
independent builds, and the release is only signed when they agree.

The builder pushes with a fine-grained token: repository access "Only
select repositories" → `gjallarOS-statements`, permission Contents "Read
and write", nothing else. It cannot touch the gjallarOS repository. The
`sign` job trusts a statement only through the signature and
`BUILDER_SSH_KEY`, not because it is in that repository.

Setting it up:

1. Create `gjallarOS-statements` as a public repository with a README, so
   `main` exists. Add a branch ruleset on `main`: block force pushes and
   deletion. The builder only ever adds a commit on top.
2. Start the builder. On first boot `gjallar-builder-statement-key.service`
   makes the key. Read the public half through the guest agent and put it
   into `BUILDER_SSH_KEY` ([step 2](#2-environment-installer-sign)):

   ```sh
   qm guest exec <vmid> -- cat /var/lib/gjallar-builder/statement-key.pub
   ```

3. Make the token on your own login, not the bot's, so the login that tags
   is not the one that writes statements: Settings → Developer settings →
   Personal access tokens → Fine-grained tokens → Generate new token.
   Token name `gjallarOS statements`, Resource owner `JadeOpenServices`,
   expiration at most one year, "Only select repositories" →
   `gjallarOS-statements`. Click "Add permissions", tick "Contents" and
   switch it to "Read and write"; "Metadata: Read-only" comes with it.
   Nothing else. If the organization lists it under Pending requests,
   approve it there. Hand the token to the builder through stdin, so it
   is on no command line. Paste it, press Enter, then Ctrl-D:

   ```sh
   qm guest exec <vmid> --pass-stdin 1 -- /run/current-system/sw/bin/gjallar-builder-set-token
   ```

   Our builder is VM 200 on pve01. From the laptop, with the typing
   hidden (`stty echo` turns it back on):

   ```sh
   stty -echo; ssh root@pve01.openjade.de qm guest exec 200 --pass-stdin 1 -- /run/current-system/sw/bin/gjallar-builder-set-token; stty echo
   ```

   It worked when the output holds `"out-data" : "token saved\n"` and
   `"exitcode" : 0`. The script accepts only one `github_pat_...` line
   and stores it in `/var/lib/gjallar-builder/statements-token`, readable
   by root only.

   Fine-grained tokens expire. Before the date GitHub shows, make a new
   one and run the same command again.

What it does: 15 minutes after boot, and then one hour after each run,
`gjallar-builder-statement.service` lists the `v*` tags and takes the
oldest one from `v2026.10.67` on that has no statement yet. One tag per
run. Older tags have no installer release and are never built.

The tagged code runs only as the user `gjallar-release-build`, which may
use Nix but is not trusted by it and cannot read the key or the token.
Root reads only `SHA256SUMS` and `RELEASE` from the build output, checks
that they are plain files of the expected shape, signs, and pushes. The
statements repository is never checked out on the builder: the job writes
the new commit straight from git objects, so a file or link someone put
into that repository never lands on the builder's disk.

To see what it did:

```sh
qm guest exec <vmid> -- journalctl -u gjallar-builder-statement -n 60 --no-pager
```

Each failed try for a tag is counted in
`/var/lib/gjallar-builder/failed/<tag>`. After three, the builder leaves
that tag alone and goes on with newer ones. Fix the cause first, then let
it try again:

```sh
qm guest exec <vmid> -- rm /var/lib/gjallar-builder/failed/vYYYY.MM.N
```

| Message in the journal | What to do |
| --- | --- |
| `no statement key yet` | The key service failed: `journalctl -u gjallar-builder-statement-key` |
| `the build of ... failed` | The journal shows the end of the build log. CI most likely fails the same way |
| `RELEASE of the build does not say version=...` | `system/release` in the tagged commit is wrong; CI stops on it too. New commit, new tag |
| `SHA256SUMS has N lines` / `SHA256SUMS line N is not the hash of ...` | The `gjallar-release` output changed shape. `statement.sh` and the workflow list the same five files and must change together |
| `... is not a regular file` / `... is larger than ... bytes` | The build put a link or an oversized file where `SHA256SUMS` or `RELEASE` belong. That is the tagged code trying something. Do not retry; find out which commit did it |
| `... in the statements repository is a blob, not a folder` | Someone put a file or link at the tag's path in gjallarOS-statements. Find out who, remove it with a commit, then clear the failed file |
| `... does not hold a fine-grained token` | Set the token again (step 3) |
| `pushing the statement for ... failed` | Token expired or without rights, or GitHub was down. The next run tries again |

Updating the builder after a change in `deployment/builder`: switch it in
place through the guest agent, with the full commit hash, so the disk and
its key stay. The guest agent finds no `nixos-rebuild` on its own path,
so the command runs through the system shell:

```sh
qm guest exec <vmid> --timeout 3600 -- /run/current-system/sw/bin/sh -c 'PATH=/run/current-system/sw/bin; nixos-rebuild switch --flake "github:JadeOpenServices/gjallarOS/<full commit>?dir=deployment/builder#gjallaros-build"'
```

Changing the builder key: a new builder disk makes a new key. Then
`BUILDER_SSH_KEY` has to change too, and `sign` refuses statements until
it does. Change it only when you know why the key changed, the same way
as the [release key](#changing-the-release-key). Statements already in
the repository stay as they are; `sign` only looks at the tag it signs.

#### 5. Check the setup

None of these print a secret. Each line says what it should show.

```sh
gh api orgs/JadeOpenServices --jq .two_factor_requirement_enabled
gh api orgs/JadeOpenServices/teams/gjallaros-release/members --jq '.[].login'
gh api orgs/JadeOpenServices/teams/gjallaros-release/repos --jq '.[] | .full_name + " " + .role_name'
gh api repos/JadeOpenServices/gjallarOS/rulesets --jq '.[].name'
gh api repos/JadeOpenServices/gjallarOS/environments/installer-sign/secrets --jq '.secrets[].name'
gh api repos/JadeOpenServices/gjallarOS/environments/installer-sign/variables --jq '.variables[].name'
gh api repos/JadeOpenServices/gjallarOS/environments/release-cache/secrets --jq '.secrets[].name'
secret-tool lookup service github.com user gjallaros-release-bot >/dev/null && echo bot token stored
git ls-files deployment/release-key.pem
```

1. `true`
2. the bot's login (`release-botGjallar`), nobody else
3. `JadeOpenServices/gjallarOS write`
4. the three rulesets of [step 3](#3-tag-and-branch-rules)
5. `RELEASE_SIGNING_KEY`
6. `BUILDER_SSH_KEY`
7. `NIX_SIGNING_KEY`
8. `bot token stored`
9. `deployment/release-key.pem`

The bot's token approval shows under Organization → Settings → Personal
access tokens → Active tokens. The builder's token shows only by working:
its journal says the statement was pushed.

### Per release

Run these from the checkout, on the branch the release goes to.

1. On the release commit, set `system/release` to the new tag name.
2. Build the makers and get the README lines. Any new file the build
   needs must be `git add`ed first (see [Release key](#1-release-key)).
   `nice`, `ionice` and the job limits keep the machine usable meanwhile:

   ```sh
   nice -n 10 ionice -c3 nix build --max-jobs 2 --cores 4 "git+file://$PWD?submodules=1#gjallar-release-makers"
   cat result/readme-fingerprint result/readme-makers
   ```

3. Put `result/readme-fingerprint` (two lines) under "Release key
   fingerprint (SHA-256):" in `README.md` and the two
   `result/readme-makers` rows into the maker table. The maker rows change
   with every release, because the makers carry the version and the
   floor. The fingerprint changes only with a new key. Check that the
   README now holds exactly those lines:

   ```sh
   grep -cFf result/readme-fingerprint README.md
   grep -cFf result/readme-makers README.md
   ```

   Both print `2`. The `gjallar-release` build in CI checks the same and
   stops on a stale README.
4. Commit and push the branch (`dev`, `staging` or `stable`) with your
   own login, then tag the commit and push the tag with the bot's token
   from the keyring. The first `credential.helper=` drops every other
   helper, so the push cannot fall back to your login. `username=` is
   the bot's GitHub login:

   ```sh
   git push origin HEAD:dev
   git tag vYYYY.MM.N <commit>
   git -c credential.helper= -c 'credential.helper=!f() { echo username=release-botGjallar; printf "password="; secret-tool lookup service github.com user gjallaros-release-bot; echo; }; f' push origin refs/tags/vYYYY.MM.N
   ```

   Without `secret-tool` on the path, put
   `nix shell nixpkgs#libsecret -c` in front of the last `git`. The tag
   starts two workflows: the installer release (`installer-iso.yml`) and
   the release cache (`cache.yml`). `build` runs for 1 to 6 hours.
5. Approve the release cache. Its job waits for the `release-cache`
   environment: open the "Release cache" run under Actions → "Review
   deployments" → tick `release-cache` → Approve. With `gh`:

   ```sh
   gh run list --repo JadeOpenServices/gjallarOS --workflow cache.yml --limit 1 --json databaseId,status,headBranch
   gh api repos/JadeOpenServices/gjallarOS/environments/release-cache --jq .id
   gh api -X POST repos/JadeOpenServices/gjallarOS/actions/runs/<run id>/pending_deployments -F 'environment_ids[]=<environment id>' -f state=approved -f comment='vYYYY.MM.N cache'
   ```

   Check first that `headBranch` is the tag you just pushed.
6. Wait for the builder statement: the folder
   `vYYYY.MM.N/gjallar-builder/` in gjallarOS-statements. The builder
   looks for new tags once an hour, then builds for about as long as CI.

   ```sh
   gh api repos/JadeOpenServices/gjallarOS-statements/contents/vYYYY.MM.N/gjallar-builder --jq '.[].name'
   ```

   It prints `SHA256SUMS` and `SHA256SUMS.sshsig` once it is there.
7. Open the installer release run, check what you are about to sign
   ([Before you approve](#before-you-approve)), and approve
   `installer-sign` the same way as in step 5. This one you approve in
   the web page, after reading the run; the bot pushed the tag, so
   "Prevent self-review" lets you.
8. `publish` puts the release online. If `stable` has the same tree, it
   becomes Latest. Check it:

   ```sh
   gh release view vYYYY.MM.N --repo JadeOpenServices/gjallarOS --json isDraft,assets --jq '.isDraft, (.assets[].name)'
   ```

   It prints `false` and the seven files of
   [What a release holds](#what-a-release-holds).

The approval waits up to 30 days. If you approve before the statement is
there, `sign` stops with "No builder statement"; use "Re-run failed jobs"
once it is there. That re-runs `sign` and `publish` with the files `build`
already made.

### Before you approve

The workflow that runs is the one in the tagged commit, `sign` included.
The approval is the only thing between that file and the signing key. So
approve only:

- a tag the release bot made, for a commit you asked it to tag,
- whose commit is on `dev`, `staging` or `stable` (the `build` log says
  which),
- whose `.github/workflows/installer-iso.yml` is unchanged since the last
  release, or changed in a way you read:

  ```sh
  git diff vLAST vYYYY.MM.N -- .github/workflows/installer-iso.yml
  ```

### When a job fails

| Message | What to do |
| --- | --- |
| Tag push: `403`, `Permission to JadeOpenServices/gjallarOS.git denied to <bot>` | The bot's token is not approved yet (Organization → Settings → Personal access tokens → Pending requests), lacks Contents "Read and write", expired, or the bot left the team `gjallaros-release` |
| Tag push: `GH013: Repository rule violations found for refs/tags/...` | The push used your own login, not the bot's; the tag ruleset lets only the team `gjallaros-release` make tags. Use the command of [Per release](#per-release) step 4 as it is, with `credential.helper=` first |
| Local build: `does not provide attribute ... gjallar-release-makers` | `deployment/release-key.pem` or another new file is not known to git: `git add` it |
| `... is not a release name like v2026.10.1` | Tags are `vYYYY.MM.N`. Delete the tag and push a correct one |
| `... points at a commit that is on none of dev, staging, stable` | Push the commit to its channel branch first, then delete and push the tag again |
| `A release or draft for ... exists` | A draft from an earlier run: delete it under Releases, then re-run. A published release stays; make a new tag |
| `system/release says ..., the tag is ...` | Bump `system/release` in the tagged commit (new commit, new tag) |
| `deployment/release-key.pem is missing` | [Release key](#1-release-key) |
| `gjallar-release: README.md ...` | The README lines are stale; [Per release](#per-release) step 2 and 3 |
| `gjallar-release: the image is over 2000000000 bytes` | The stick image grew past what the makers write; make it smaller |
| `No builder statement for ... yet` | Wait for the builder, then "Re-run failed jobs" |
| `... is not signed by the pinned builder key` | Wrong or changed builder key. Check `BUILDER_SSH_KEY` against the builder; do not just replace it |
| `The builder got other files than CI` | The two builds differ, so the build is not reproducible or one side is wrong. The diff shows which file. Find out why before anything else; never sign around it |
| `RELEASE_SIGNING_KEY does not belong to deployment/release-key.pem` | The secret and the repository key differ: one of them was changed without the other |
| `The draft for ... holds: ...` / `SHA256SUMS in the draft is not the one that was signed` | Someone changed the draft after `build`. Find out who, delete the draft, start again |
| `publish` fails after it uploaded `SHA256SUMS.sig` | Delete `SHA256SUMS.sig` from the draft, then "Re-run failed jobs" |

If only "Prune old releases" failed, the release is out; the next release
prunes again.

### Latest and stable

New sticks get Latest, and Latest is always the release that `stable`
runs. `publish` marks a release as Latest when `stable` has the same tree
as its tag. When `stable` moves later (`git push origin <commit>:stable`),
`promote` finds the published release with that tree and marks it. A
`dev` or `staging` release is published but not Latest; the makers reach it
with `--release vYYYY.MM.N` / `-Release vYYYY.MM.N`.

If `promote` says "No published installer release has the tree of stable",
`stable` points at a tree that was never released this way. Latest then
stays where it was.

### Old releases

`publish` keeps the three newest published releases that carry
`SHA256SUMS.sig`, plus Latest, and deletes the older ones. The git tags
stay. It never touches `nix-cache`, drafts or releases without a
signature. A maker asked for a deleted release stops with a download
error.

### Release floor

`deployment/release-floor` names the oldest release still safe to write.
It goes into `RELEASE` and into the makers. A maker refuses to write a
release older than the newest floor it has seen, and the computer
remembers that floor. Raise it when an older release has a problem that a
fresh install must not get, in the same commit as a new release. The
build refuses a floor newer than the release itself.

### Changing the release key

1. Make a new key as in [Release key](#1-release-key).
2. In one commit: replace `deployment/release-key.pem`, update the README
   fingerprint and maker rows, bump `system/release`.
3. Replace the secret `RELEASE_SIGNING_KEY`, then tag.

From then on only the new makers accept new releases. Anyone with an old
maker gets a signature error and fetches the maker again; the README shows
the new fingerprint. An old maker still accepts old releases signed with
the old key, and nothing can take that back. If the old private key
leaked, whoever holds it can sign a release that old makers accept; they
still need write access to the releases here to put it where the makers
look. Say so in the release notes and at the top of the README, so people
fetch the maker again.
