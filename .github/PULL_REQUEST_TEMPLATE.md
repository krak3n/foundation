<!-- The title is a conventional commit: type(scope): subject (see CLAUDE.md, "Pull requests"). -->

## What

<!-- What changes, in a few lines. One purpose per pull request. -->

## Why

<!-- The problem it solves, or the decision it carries out. -->

## How it was verified

<!-- The commands that were run and what they said, including anything that was skipped
or could not be run. Do not claim more than was run. -->

- [ ] `make vet`, `make test` and `make staticcheck` pass
- [ ] `make test-scripts` and `make lint-workflows` pass (if scripts or workflows changed)
- [ ] `make check-commits` passes

## Security

<!-- Who controls each new input, what an attacker gains, what this does about it, and
which hostile-input or fuzz tests cover it. "No new input" is an answer. -->

## Review

<!-- The adversarial review of the whole branch diff, done before the push (CLAUDE.md,
"Pull requests"): how many findings, which were fixed, which were left and why. -->

- [ ] Docs and examples (README, `CLAUDE.md`, `CONFIG.md`) match the change
