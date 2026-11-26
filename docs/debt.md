# Debt register

`tests/debt/register.json` lists the code gjallarOS carries that should go
one day: every patch to a package and every open workaround. Each entry says
why it stays, how it goes away and when someone looks at it again. The flake
check `debt` holds the list against the tree.

## The check fails when

- a tracked `*.patch` file is in no entry, or in two
- a member is missing, or is a link, a FIFO or anything but a regular file
  in the tree
- a patch entry has no guard, or its guard file has no version assert,
  pinned rev or source hash
- a tier 1 entry has no test, or the test is not a check in `flake.nix`
- a tier 1 review date has passed, or is more than 30 days ahead
- a review date moved later without a new `why` and a higher `renewed`
- an entry has been renewed three times or more without Jade's OK

Tier 3 problems only print a warning. A tier 1 review due within 7 days also
prints a warning.

## Running it

```sh
nix build --no-link -L "git+file://$PWD#checks.x86_64-linux.debt"
```

From a checkout, without a build:

```sh
python3 -I tests/debt/debt.py .
python3 -I tests/debt/debt.py . --today 2026-12-30
```

`--today` runs the date rules as on another day, to see what runs out
before a release. The renewal rule compares with `HEAD`, so it works only
in a checkout, before you commit.

The workflow `.github/workflows/upkeep.yml` runs this check with `copies`
and `legacy-names` on every push to `dev`, on every pull request and once a
week, so a date that runs out between pushes still shows up.

## Tiers

| Tier | What | Needs | Review |
| --- | --- | --- | --- |
| 1 | Security path | guard, guard test, `review` date | at most 30 days ahead; every release runs the check again |
| 2 | Upgrade debt | guard | at every bump of the patched package; the guard stops the build when the source moves |
| 3 | Hygiene | nothing more | none; problems only warn |

Security problems found in a review go to the private security review page,
never into this file or the repo.

## An entry

```json
{
  "name": "example-tool",
  "kind": "patches",
  "members": ["pkgs/patches/example-tool-fix.patch"],
  "tier": 2,
  "item": 999,
  "why": "What breaks without it, in one line.",
  "exit": "When it can go: upstream takes it, or the package leaves",
  "guard": "pkgs/overlays/example-tool.nix"
}
```

| Field | Meaning |
| --- | --- |
| `name` | Unique; for a workaround the W number and a short name |
| `kind` | `patches` or `workaround` |
| `members` | Paths in the tree; every patch file of the set, or the files that hold the workaround |
| `tier` | 1, 2 or 3 |
| `item` | The Plane item that tracks it |
| `why` | Why it stays |
| `exit` | What has to happen before it can go |
| `guard` | Patches only: the file that pins or asserts the patched source, or `Plane N` while the item that adds one is open |
| `test` | Tier 1 only: a `tests/nix/*.nix` check wired in `flake.nix`, or `Plane N` while the item that adds one is open |
| `review` | Tier 1 only: `YYYY-MM-DD` |
| `renewed` | Tier 1 only: how often the review date moved later; 0 when left out |

## Adding a patch

1. Add the patch and its guard: a version assert in the overlay
   (`assert old.version == "1.2.3";`), a pinned `rev`, or a source hash
2. Add an entry, or add the file to the members of the package's entry
3. On a security path use tier 1, add the test that proves the fix, and set
   `review` at most 30 days ahead
4. Write the patch up for upstream (book 753), as for every patch

## Renewing a tier 1 review

When a review date comes up:

1. Look at the debt again: is upstream ready, is the guard test still
   testing the right thing, is the exit still true?
2. If it can go, remove the patch or workaround and the entry
3. If it stays, set a new `review` date at most 30 days ahead, change `why`
   to what the review found, and raise `renewed` by one

The third renewal needs Jade's OK. Put it in the `why`, for example
`... Jade 2026-12-20`.

## Removing an entry

Remove the entry in the same commit that removes the patch or the workaround,
and close the Plane item. A member that is gone fails the check, so an entry
cannot outlive its code.
