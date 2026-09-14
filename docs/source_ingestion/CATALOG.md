# CATALOG — GitHub repositories containing Skills for CLI AI agents

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z
**Author:** research subagent (T?/main - claude1), BACKGROUND research task
**Method:** WebSearch + WebFetch (§11.4.8 / §11.4.99 / §11.4.150). Every entry is a REAL repo actually found; facts (stars, counts, license, layout) are as reported by the repo's own README/GitHub page at fetch time (2026-07-16). Numbers a repo self-reports (skill counts) are labeled "self-reported". Anything not directly verified is marked `UNCONFIRMED` (§11.4.6 — no invention).

---

## 0. Executive summary

- **12 real repositories** cataloged below, plus the discovery surfaces (GitHub topic pages, agentskills.io standard).
- The ecosystem splits into **three structural classes**, and this distinction is the single most important design input:
  - **(V) Vendored** — the repo physically contains the `SKILL.md` files. These are the direct fetch/parse targets. (anthropics/skills, obra/superpowers, sickn33/agentic-awesome-skills, jeremylongshore/…, alirezarezvani/…)
  - **(L) Link-index / curated list** — the repo is a README of links pointing to OTHER repos that hold the skills. A source-ingestion system pointed at these must **traverse to the linked source repos**, not parse the list's own markdown as skills. (VoltAgent/…, travisvn/…, karanb192/…, heilcheng/…)
  - **(M) Marketplace** — a `.claude-plugin/marketplace.json` describing plugins whose `source` fields point at git repos/subdirs that hold skills. (anthropics/skills, anthropics/claude-plugins-official, jeremylongshore/…)
  - Many repos are more than one class (e.g. anthropics/skills is V + M).
- **Canonical on-disk format** (verified from official docs + anthropics/skills): a skill is a **folder** `<skill-name>/` containing `SKILL.md` (YAML frontmatter + markdown body), optionally `scripts/`, `references/`, `assets/`. Format details in §2.

---

## 1. Repository catalog

### TIER A — Official / foundational (highest trust, primary import targets)

#### A1. `anthropics/skills` — Public repository for Agent Skills  ⭐ ~162k
- URL: https://github.com/anthropics/skills
- Class: **V + M** (vendors skills AND ships `.claude-plugin/marketplace.json`).
- Contents: skills organized under `skills/` in categories **Creative & Design**, **Development & Technical**, **Enterprise & Communication**, and **Document Skills** — the production document set `skills/docx`, `skills/pdf`, `skills/pptx`, `skills/xlsx`. Also `skills/skill-creator` (scaffolder). ~43 commits (small, curated).
- Layout: `.claude-plugin/`, `skills/` (folder-per-skill, each with `SKILL.md`), `spec/` (the Agent Skills spec), `template/` (skill template), `THIRD_PARTY_NOTICES.md`.
- Frontmatter: required `name`, `description`; optional `license`. (Full field set in §2.)
- LICENSE: **mixed** — many skills **Apache-2.0**; the document skills (`docx/pdf/pptx/xlsx`) are **source-available, NOT open source** (import/redistribution caution — see §4 licensing risk).
- Notable/innovative: `skill-creator` (meta-skill), the four document skills (real production implementations, not toys), the `spec/` directory (authoritative format).

#### A2. `anthropics/claude-plugins-official` — official Claude plugin marketplace  (CONFIRMED: large index)
- URL: https://github.com/anthropics/claude-plugins-official
- Class: **M**. Root `.claude-plugin/marketplace.json` (~144 KB, 3,514 lines → **several hundred plugins**; ≥71 confirmed in the fetched head alone). Each plugin `source` points at a git repo (or a bundled `./plugins/<x>` subdir).
- Value: **the single highest-value seed list** — it maps official plugins to their vendor skill repos. The `source` URLs follow a clear pattern (`<vendor>/skills.git`, `<vendor>/agent-skills.git`, `<vendor>/claude-plugin.git`), e.g. `adobe/skills`, `Airtable/skills`, `apollographql/skills`, `cloudflare/skills`, `auth0/agent-skills`, `microsoft/azure-skills`, `ClickHouse/agent-skills`, `canva-sdks/canva-skills`, `box/box-for-ai`, `brightdata/skills`, `buildkite/skills`, `coderabbitai/skills`, `CrowdStrike/foundry-skills`, `get-convex/convex-backend-skill`, plus AWS/Google-Gemini extension families. Registering this ONE marketplace (and resolving its `source` links, class M→V traversal) discovers dozens of authoritative vendor skill repos out of the box.

#### A3. `obra/superpowers` — agentic skills framework & dev methodology  ⭐ (battle-tested; already vendored by THIS project as the `superpowers:` skills)
- URL: https://github.com/obra/superpowers
- Class: **V**. Fourteen skills, each a single `SKILL.md` with YAML frontmatter + a few hundred words; plus a session-start hook that bootstraps skill use. Multi-host (Claude Code, Antigravity, Codex App/CLI, Cursor, Factory Droid, GitHub Copilot CLI, Kimi Code, OpenCode, Pi).
- Notable skills: `systematic-debugging` (4-phase root-cause, "no fixes without understanding"), `brainstorming`, `writing-plans`, `test-driven-development`, `using-git-worktrees`, `finishing-a-development-branch`, `subagent-driven-development`, `dispatching-parallel-agents`, `requesting/receiving-code-review`, `verification-before-completion`, `using-superpowers`, `condition-based-waiting`, `root-cause-tracing`, `defense-in-depth`.
- Why it matters here: this repo's methodology skills are the **"game-changer deltas"** class — our System's constitution already references `superpowers:systematic-debugging` (§11.4.102). Diffing our skills against this repo is the highest-value enhancement lane.
- Related mirrors/forks (lower priority): `Hacker0x01/claude-power-user`, `richvieren/superpower-skills` (both "Claude Code superpowers: core skills library"); `obra/superpowers-developing-for-claude-code` (companion).

### TIER B — Mega libraries / installable collections (bulk import candidates)

#### B1. `sickn33/agentic-awesome-skills` — installable 1,900+ SKILL.md library  ⭐ ~43.4k
- URL: https://github.com/sickn33/agentic-awesome-skills
- Class: **V** (vendors the actual `SKILL.md` files). Layout `skills/<skill-name>/SKILL.md`. Ships a **`skills_index.json` stable manifest** (machine-readable metadata — a ready-made discovery index), role **bundles**, ordered **workflows**, and 13+ domain **plugin** distributions. Installer: `npx agentic-awesome-skills`.
- Self-reported count: **1,963+ skills**. Activity: **v14.5.0 released 2026-07-15** (one day before this research), 2,261 commits, 167 releases, 6.6k forks — **very active**.
- LICENSE: **MIT** (code) + **CC BY 4.0** (documentation/content), with an attribution ledger `docs/sources/sources.md`.
- Notable/innovative: deterministic workflow skills — `lemmaly` (algorithm-first discipline), "DOS kernel" (git-verified ship claims), `RecallMax` (context compression), `scopeblind` (multi-agent governance), `polis-protocol` (routing history). Browser automation (Browserbase), video extraction (FFmpeg/Tesseract).
- Caveat (§4): being mass-aggregated, quality/originality varies — dedup + quality-gate on import is essential.

#### B2. `VoltAgent/awesome-agent-skills` — curated directory of 1,497+ skills  ⭐
- URL: https://github.com/VoltAgent/awesome-agent-skills
- Class: **L** (curated **directory of links**, NOT vendored — points at vendor repos / officialskills.sh URLs). Categorized by ORG: Anthropic, Microsoft (133 skills / 6 languages), Google Gemini, Vercel, Cloudflare, Netlify, OpenAI, Figma, Hugging Face, Trail of Bits, Sentry, HashiCorp, Stripe, +50 more.
- Self-reported count: **1,497+**. Activity: 423 commits, sponsor-backed, actively maintained. LICENSE: **MIT**.
- Value: best **source-of-sources** — traverse its links to discover authoritative vendor skill repos to register individually.

#### B3. `jeremylongshore/claude-code-plugins-plus-skills` — marketplace, 2,810 skills  ⭐ ~2.5k
- URL: https://github.com/jeremylongshore/claude-code-plugins-plus-skills
- Class: **V + M**. Layout `/plugins`, `/skills` (vendored `SKILL.md`), `/marketplace` (marketplace.json), `/templates`. `ccpi` CLI package manager; site tonsofskills.com.
- Self-reported: 425 plugins / 2,810 skills / 200 agents (badges show higher: 470 / 3,677 / 347). 18 categories. Activity: v4.33.0, 1,564 commits, 452 npm packages — active. LICENSE: **MIT**.
- Notable: enforces an **8-field frontmatter** — `name / description / allowed-tools / version / author / license / compatibility / tags` (a superset schema worth supporting in our parser). Skills: `openrouter-pack`, `databricks-pack`, `wallet-security-auditor`.

### TIER C — Curated lists / indexes (source discovery, not bulk import)

#### C1. `travisvn/awesome-claude-skills` — curated list  ⭐ ~14.1k
- URL: https://github.com/travisvn/awesome-claude-skills
- Class: **L**. ~13 official + 20+ community entries + tutorials/guides/security articles. Links primarily to `anthropics/skills` and `obra/superpowers`. 42 commits, last update ~Feb 2026. LICENSE: `UNCONFIRMED` (not stated in fetched content).

#### C2. `karanb192/awesome-claude-skills` — 50+ verified curated list  ⭐ ~430
- URL: https://github.com/karanb192/awesome-claude-skills
- Class: **L**. 11 categories (Document/File, Testing&Quality, Debugging, Collaboration, Dev&Architecture, Security&Performance, Documentation, Media, Data, Writing&Research, Meta). Verification badge per entry (many still "Community-needed"). LICENSE: **MIT**. Top verified: `test-driven-development`, `systematic-debugging`, `using-git-worktrees`, `artifacts-builder`.

#### C3. `alirezarezvani/claude-skills` — 345 skills/agents/plugins  `layout UNCONFIRMED`
- URL: https://github.com/alirezarezvani/claude-skills
- Class: **V** (has a top-level `CLAUDE.md`; self-reports 30+ agents / 70+ commands / 330+ skills across 12+ coding agents; domains: engineering, marketing, product, compliance, C-level, research, ops, finance). Exact license + on-disk layout **UNCONFIRMED** (not fetched in full this round).

#### C4. `GetBindu/awesome-claude-code-and-skills` — collection  `UNCONFIRMED`
- URL: https://github.com/GetBindu/awesome-claude-code-and-skills
- Class: collection of Claude Skills; details **UNCONFIRMED** (found via search, not fetched).

#### C5. `heilcheng/awesome-agent-skills` — tutorials/guides/directories  `UNCONFIRMED`
- URL: https://github.com/heilcheng/awesome-agent-skills
- Class: **L** + educational; details **UNCONFIRMED** (found via search, not fetched).

### Discovery surfaces (not repos to import, but crawl seeds)
- `https://github.com/topics/claude-code-skills` — GitHub topic page: live list of repos tagged `claude-code-skills`. A periodic crawler seed for auto-discovering NEW source repos.
- `https://agentskills.io` — the open **Agent Skills** standard (the format the ecosystem targets). Authoritative spec reference.
- `https://code.claude.com/docs/en/skills` — Claude Code's skill docs (frontmatter reference, invocation control, subagent execution).

---

## 2. On-disk SKILL.md format (verified — authoritative for the parser)

**Directory layout** (per official docs + anthropics/skills):
```
<skill-name>/
  SKILL.md          # required: YAML frontmatter + markdown body
  scripts/          # optional: executable helpers (python/shell)
  references/       # optional: long docs, loaded on demand (progressive disclosure)
  assets/           # optional: templates, fixtures, images
```
Skills live in `~/.claude/skills/` (personal), `<project>/.claude/skills/` (project), or inside a plugin (`${CLAUDE_PLUGIN_ROOT}/skills/...`). Directory name → the `/command`.

**Frontmatter fields** (Claude Code, verified from code.claude.com/docs/en/skills, 2026-07-16):
| Field | Req? | Notes |
|---|---|---|
| `name` | required | kebab-case `[a-z0-9-]+`. Display label; for a plugin-root SKILL.md it sets the command name (else the directory name does). |
| `description` | recommended | What it does + when to use it (drives auto-invocation). Combined `description`+when-to-use **truncated at 1,536 chars** in the skill listing. If omitted, first markdown paragraph is used. |
| `license` | optional | e.g. `Apache-2.0`, `Source-available` (from anthropics/skills spec). |
| `disable-model-invocation` | optional (bool) | `true` = only user can invoke (`/name`); hides from auto-load + subagent preload + scheduled tasks. |
| `allowed-tools` | optional | Tools grantable without prompting while active. Space/comma/YAML-list. |
| `disallowed-tools` | optional | Tools removed from the pool while active. |
| `context: fork` | optional | Run the skill in an isolated subagent (skill body becomes the subagent prompt). |
| `arguments` | optional (list) | Named args, positional mapping (`arguments: [issue, branch]` → `$issue`, `$branch`). |
| `user-invocable` | optional | Controls `/skills` menu visibility only (not Skill-tool access). |

**Open-standard core** (agentskills.io, cross-tool): `name` + `description` are the portable minimum; Claude Code, Cursor, Codex, Gemini CLI etc. each extend it. Some marketplaces enforce a superset (jeremylongshore's **8-field**: `name/description/allowed-tools/version/author/license/compatibility/tags`). **Design implication:** the parser must accept the 2-field minimum, tolerate unknown fields (store them), and specifically capture `name`, `description`, `license`, `allowed-tools`, plus body + `scripts/references/assets` presence.

**Body:** markdown instructions (the "how"). References/scripts/assets are referenced from the body and loaded on demand (progressive disclosure).

---

## 3. Cross-repo facts that shape the design (§4 risk inputs)

1. **Vendored vs link-index (V/L/M) must be auto-detected** — pointing the ingester at a Tier-C list and parsing its README as skills would import garbage. Detect: presence of `SKILL.md` files (V) vs a `.claude-plugin/marketplace.json` (M) vs an `awesome-*` README of links (L → resolve links, then treat each as a new source).
2. **Scale is large** — a single mega-repo (sickn33, jeremylongshore) holds 1,900–3,700 skills. Import + dedup + quality-gate must be batched, idempotent, and rate-limit aware (GitHub API 5,000 req/h authenticated).
3. **Duplication across repos is massive** — the SAME skill (`systematic-debugging`, `test-driven-development`) appears in anthropics, obra/superpowers, and every curated list. Dedup by normalized name AND content-hash is mandatory to avoid N copies (§ DESIGN dedup policy).
4. **License heterogeneity** — Apache-2.0, MIT, CC BY 4.0, and **source-available (NOT open source)** (anthropics document skills) coexist. Import MUST capture per-skill license provenance and MUST NOT redistribute source-available content as if open (legal risk — see DESIGN §"licensing").
5. **Change cadence varies wildly** — anthropics/skills is slow (~43 commits); sickn33 ships releases daily (v14.5.0 the day before this research). Re-scan cadence should be per-source configurable, keyed off commit SHA (§11.4.86 content-hash change detection).
6. **The frontmatter is a loose contract** — 2 required fields, many optional, unknown fields common. Parser must be permissive-but-lossless (store the raw frontmatter map + the parsed body).

---

## Sources verified 2026-07-16
- https://github.com/anthropics/skills
- https://github.com/anthropics/claude-plugins-official
- https://github.com/obra/superpowers
- https://github.com/obra/superpowers/blob/main/.claude-plugin/plugin.json
- https://github.com/Hacker0x01/claude-power-user
- https://github.com/richvieren/superpower-skills
- https://github.com/VoltAgent/awesome-agent-skills
- https://github.com/sickn33/agentic-awesome-skills
- https://github.com/jeremylongshore/claude-code-plugins-plus-skills
- https://github.com/travisvn/awesome-claude-skills
- https://github.com/karanb192/awesome-claude-skills
- https://github.com/alirezarezvani/claude-skills
- https://github.com/GetBindu/awesome-claude-code-and-skills
- https://github.com/heilcheng/awesome-agent-skills
- https://github.com/topics/claude-code-skills
- https://code.claude.com/docs/en/skills
- https://code.claude.com/docs/en/plugin-marketplaces
- https://github.com/anthropics/claude-code/blob/main/.claude-plugin/marketplace.json
- https://deepwiki.com/anthropics/skills/2.2-skill.md-format-specification
- https://agentskills.io  (Agent Skills open standard; referenced by code.claude.com docs)
