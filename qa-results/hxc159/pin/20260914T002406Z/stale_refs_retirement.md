# T-P3.03.2 — stale-ref retirement decision

All four stale refs verified `git log main..<ref>` EMPTY + ancestor of main
(see `stale_refs_gitlog.txt`):

- `origin/helix_skills` @ `06d77bd8` (108 behind main)
- `origin/worktree-agent-a41ff2c21450bdf5c` @ `25cb8ca0` (55 behind)
- `origin/worktree-agent-abb59d8974644d7a9` @ `25cb8ca0` (same SHA)
- `origin/worktree-agent-adb9e192e0bc9275c` @ `25cb8ca0` (same SHA)

Retirement = remote branch deletion, which requires a push. This work order
forbids pushing ("push NOTHING — conductor reviews then pushes"), so the
deletions are RECORDED here and DEFERRED to the conductor's push step:

- `git push origin --delete helix_skills`
- `git push origin --delete worktree-agent-a41ff2c21450bdf5c`
- `git push origin --delete worktree-agent-abb59d8974644d7a9`
- `git push origin --delete worktree-agent-adb9e192e0bc9275c`

No local branches exist for these refs (only remote-tracking), so there is
nothing to retire locally. U-8 CLOSES on this evidence: the two distinct
diffs (`25cb8ca0`, `06d77bd8`) were both read as empty-vs-main.
