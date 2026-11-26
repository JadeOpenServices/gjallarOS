# Copied code

The flake check `copies` finds code that appears twice and fails on every
copy that `tests/copies/known.json` does not list. Copied code drifts: a fix
lands in one copy and the other keeps the bug. The rule:

- Security code (anything on a root, TPM, LUKS, Secure Boot, USB or polkit
  path) is shared at its second copy
- Everything else is shared at its third copy
- A copy that stays gets a row in the list with the reason and the Plane item
  that removes it

## What counts as a copy

| Kind | What it is |
| --- | --- |
| `whole` | A function body of 4 or more code lines that is the same as another body |
| `run` | 8 or more code lines in a row that are the same in otherwise different bodies |

In Go, Python, JavaScript and Lua a body is one function. In Nix, shell, QML
and Luau the whole file is one body. Lines are compared with whitespace
collapsed. Blank lines, comments, lone brackets and the function signature
do not count. Tests (`tests/`, `testdata/`, `*_test.go`, `test_*.py`) are
left out.

## Running it

```sh
nix build --no-link -L "git+file://$PWD#checks.x86_64-linux.copies"
```

From a checkout, without a build (needs `lizard` on the path):

```sh
python3 -I tests/copies/copies.py .
python3 -I tests/copies/copies.py . --list
```

`--list` prints every copy found, listed or not. In a checkout the script
reads tracked files only.

## A new copy

When the check fails with `new run copy: ...`:

1. Share the code if you can; that is the normal fix
2. If it has to stay for now, open a Plane item for sharing it, then print
   the rows to fill in

   ```sh
   python3 -I tests/copies/copies.py . --new
   ```

3. Paste the row into `tests/copies/known.json` and fill in `tier`, `item`
   and `why`; on tier 1 also `review`, a date at most 30 days ahead
4. Put the list back in order

   ```sh
   python3 -I tests/copies/copies.py . --sort
   ```

A row:

```json
{
  "kind": "run",
  "members": [
    "cmd/example/a.go loadThing",
    "cmd/example/b.go loadOther"
  ],
  "tier": 2,
  "item": "999",
  "why": "Not shared yet; share the loader of a and b."
}
```

A member is `path name` for a function and the plain path for a whole file.
The second function of the same name in one file is `path name #2`.

## Tiers

| Tier | When | Review |
| --- | --- | --- |
| 1 | Security path | `review` date at most 30 days ahead; the `debt` check fails once it has passed |
| 2 | Anything else that has to be shared | No date; the Plane item tracks it |
| 3 | Hygiene | None |

Moving a tier 1 review date later follows the renewal rule of the debt
register: a new `why` that says what the review found, and a higher
`renewed` count. See [Debt register](debt.md#renewing-a-tier-1-review).

## A copy that is gone

When the check fails with `listed ... copy not found`, one side changed or
the copy was shared. Remove the row, or fix its members if a function was
only renamed. Close the Plane item when the copy is really gone.

## Where the rules come from

The copy rules match JODS `scripts/lint/copies`, so both projects list copies
the same way.
