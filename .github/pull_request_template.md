## What changes

<!-- The behaviour that is different afterwards, and why it had to be. -->

## Verified

<!-- CONTRIBUTING.md explains what each of these is for. Tick what you ran; CI
     runs the first two regardless. Delete the ones that do not apply. -->

- [ ] `make test lint emit` and `git status --porcelain` is empty
- [ ] `cd installer && npm test`
- [ ] `NOETIVE_KEY_SECRET=keyu_... integration/run.sh` — touches the wire
- [ ] `node scripts/mutation-test.js --file <changed>` — adds behaviour

## Load-bearing

<!-- CONTRIBUTING.md lists four invariants under "Things that are load-bearing".
     Name the one this change comes near, or "none". A change that relaxes one
     needs to say so here, not only in the diff. -->
