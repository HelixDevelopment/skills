# T-P3.03.3 — `feature/catalog-docs` §11.4.124 investigate-before-remove

Branch: `origin/feature/catalog-docs` @ `dd1edaa5` (1 commit ahead of main @ `315b56ce`).
Net deletion: 6 files under `docs/skills/skill/` (852 deletions total in branch).

## Deleted files

- `docs/skills/skill/action_prefix_system.md` (141 lines)
- `docs/skills/skill/media_validator.md` (120 lines)
- `docs/skills/skill/reporting_workable_items.md` (135 lines)
- `docs/skills/skill/scheduled_work_queue.md` (136 lines)
- `docs/skills/skill/session_sync.md` (134 lines)
- `docs/skills/skill/workable_item_lifecycle.md` (152 lines)

## (1) WHERE/HOW originally wired

All six created as expanded skill detail pages in `b54f4fb`
("docs: expand skill detail pages ...") / `9001bd2`. Each documents one
skill; each has a kebab-case twin that SURVIVES on the branch:

| removed (snake_case) | retained (kebab-case, GENERATED canonical) |
|---|---|
| `action_prefix_system.md` | `action-prefix-system.md` |
| `media_validator.md` | `media-validator.md` |
| `reporting_workable_items.md` | `reporting-workable-items.md` |
| `scheduled_work_queue.md` | `scheduled-work-queue.md` |
| `session_sync.md` | `session-sync.md` |
| `workable_item_lifecycle.md` | `workable-item-lifecycle.md` |

Retained twins carry the `GENERATED FILE — DO NOT HAND-EDIT ... regenerated
from the live skill graph by the skills-catalog generator` header and the
identical `Name:` identity (spot-checked `action-prefix-system`: same
`Name: action-prefix-system` in both). The generator owns the kebab-case
namespace, so the snake_case copies are the redundant leg.

## (2) WHEN/HOW it became dead

Commit `dd1edaa` ("feat(catalog-docs): expand skills catalog + finalize G40
Phase 0") states: *"Removed 6 duplicate snake_case/kebab-case skill detail
pages"* and *"Fixed 14 broken cross-reference links"*. The duplication
existed since `b54f4fb`; the branch dedupes to the canonical generated page.

## (3) Hidden references

`git grep -l <snake_basename> origin/feature/catalog-docs -- docs/` returns
EMPTY for all six basenames — zero dangling references on the branch.
Markdown docs (no reflection / dynamic dispatch / codegen surface).

## Verdict

Removal PROVEN genuinely-unneeded-duplicate: same skill identity retained in
canonical generated twin + zero references + rationale recorded in-commit.
Merge proceeds (history preserved — recoverable via `dd1edaa^` regardless).

## Constitution gitlink side-effect (merge resolution)

`dd1edaa` incidentally bumped the `constitution` gitlink `8900143 → d071353`
(parent `dd1edaa^` already at `8900143`; commit message never mentions
constitution). Current main pins `68875c7a` via deliberate bump commits
(`a44d0ec`, ...). `d071353` resolves to NO local object and is unreachable
for verification here, and main's pin is NEWER. Merge resolution: keep
main's side (`--ours`) for the `constitution` path — no pin regression.
