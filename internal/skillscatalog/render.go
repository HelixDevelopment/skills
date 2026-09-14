package skillscatalog

import (
	"fmt"
	"sort"
	"strings"

	"github.com/HelixDevelopment/skills/internal/models"
)

// generatedBanner is the fixed, generator-written notice DESIGN.md §2.5
// mandates on every generated file, directly below its H1 title.
const generatedBanner = "> **GENERATED FILE — DO NOT HAND-EDIT.** Regenerated from the live skill\n" +
	"> graph by the `skills-catalog` generator. Edit the skill via CLI/REST/MCP\n" +
	"> (see `docs/scripts/` / `docs/API.md`) — this file will be overwritten.\n"

// escapeMDCell makes a free-text value safe to embed as a single Markdown
// table cell (F4 review finding, 2026-07-16; extended by the F-C review
// finding, round 3, 2026-07-16; extended AGAIN by the Finding 2 review
// finding, round 5, 2026-07-16). Skill Name/Title/Domain/Complexity/Version
// and resource Title/URL/ResourceType are all real, operator/skill-author-
// supplied free text with NO charset constraint (model.go's slugify
// docstring notes skills.name itself has none beyond TEXT UNIQUE) -- an
// unescaped "|" would be misread as a column boundary by any Markdown
// renderer, silently corrupting every column to its right in that row, and
// a raw newline would break the row (and likely the whole table) apart
// entirely. Backslash is escaped FIRST so this function's own inserted
// "\|"/"\["/"\]" escapes are never themselves re-escaped.
//
// "<" and ">" are ALSO escaped to their HTML entities (F-C fix): every
// Markdown renderer this catalog targets (GitHub/GitLab flavour and
// CommonMark's own "raw HTML" extension both included) passes bare "<...>"
// runs straight through as literal HTML -- neither backslash-escaping NOR
// the pipe/newline defences above touch that class at all, so a Name like
// "<img src=x onerror=...>" previously survived byte-for-byte into the
// rendered table cell under a raw-HTML-passthrough renderer. Escaping to
// "&lt;"/"&gt;" makes the value render as inert visible text everywhere,
// exactly like every other defence in this function.
//
// "[" and "]" are ALSO escaped (Finding 2 fix, round 5, 2026-07-16): GFM and
// CommonMark recognize "[text](url)" inline-link syntax inside EVERY inline
// context a table cell is -- including a plain, non-link table cell. A
// value such as Title = "[Download update](https://evil.example)" contains
// no character either PRIOR defence above touches (no "|", no raw newline,
// no "<"/">"), so it previously passed byte-for-byte into every table cell
// this function guards -- INDEX.md's Name/Domain/Complexity/Version
// columns, by-domain/by-kind's Name/Title columns, and the skill detail
// page's Resources (Title/URL/Type) table -- and rendered as a LIVE
// hyperlink with fully attacker-chosen link text on a page this generator
// itself controls.
// Escaping "[" to "\[" (matching this function's existing "\|" convention)
// makes "](" un-manufacturable from a free-text value alone, closing the
// injection at its root rather than at any one specific call site.
//
// Honest boundary (§11.4.6): this closes attacker-CHOSEN link TEXT only.
// GFM's extended autolinking (turning a BARE "https://..." URL appearing in
// prose into a live link with no "[...]" syntax at all) is unaffected by any
// escaping this function does or could do -- it is not a "[text](url)"
// construct, so there is no "[" or "]" for this defence to intercept. That
// residual case is out of this fix's scope (this generator never claims to
// suppress GFM autolinking of a bare URL in free text it renders verbatim).
func escapeMDCell(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "[", `\[`)
	s = strings.ReplaceAll(s, "]", `\]`)
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// escapeMDInline makes a free-text value safe to embed as inline Markdown
// prose OUTSIDE a table cell -- a heading title, an unordered-list value, or
// the visible text of a link/code-span (F3 review finding, round 2,
// 2026-07-16; extended by the F-C review finding, round 3, 2026-07-16;
// extended AGAIN by the Finding 2 review finding, round 5, 2026-07-16).
// Every skill Name/Title/Version/Domain/Complexity/Tag -- and every
// dependency/dependent Name -- is real, operator/skill-author-supplied
// free text with NO charset constraint (model.go's slugify docstring), so a
// value such as "X\n# Injected" is fully legal input.
//
// Four independent defences, applied in this order:
//  1. Collapse every CR/LF sequence to a single space. This is the
//     LOAD-BEARING fix: a Markdown heading/list-item/blockquote can only be
//     reinterpreted at the START of a physical line, so removing every
//     embedded newline means a value can never manufacture a NEW line the
//     renderer would treat as heading/list/blockquote syntax, no matter what
//     characters follow it.
//  2. Backslash-escape the ASCII characters that are Markdown-significant
//     WITHIN a single line: backtick (breaks/extends an inline code span --
//     see the NOTE below), asterisk, underscore, and GFM tilde (each opens
//     an unbalanced emphasis/strong/strikethrough marker), and a LEADING
//     '#' (would be read as an ATX heading marker if the value is ever the
//     first thing on its own line -- defence-in-depth alongside (1), since
//     a future render call site could embed the value at column 0 of a
//     line). Backslash itself is escaped FIRST so this function's own
//     inserted "\`"/"\*"/"\_"/"\~"/"\#"/"\["/"\]" escapes are never
//     themselves re-escaped (matching escapeMDCell's existing convention).
//  3. Escape "<" and ">" to their HTML entities (F-C fix, round 3,
//     2026-07-16): every Markdown renderer this catalog targets passes bare
//     "<...>" runs straight through as literal raw HTML -- a value like
//     "<img src=x onerror=...>" is untouched by defences (1)/(2) above and
//     previously survived byte-for-byte into rendered prose. "&lt;"/"&gt;"
//     render as inert visible text everywhere instead.
//  4. Escape "[" and "]" (Finding 2 fix, round 5, 2026-07-16 -- see the
//     paragraph below this list for why this defence was ADDED here, not
//     merely left to escapeMDLinkText as before).
//
// NOTE on backtick escaping (F-D review finding, round 3, 2026-07-16,
// comment-accuracy-only): CommonMark code spans do NOT honour backslash
// escapes -- a backtick preceded by "\" still counts as a REAL backtick and
// can still terminate or extend a surrounding code span early (spec: a code
// span's extent is determined purely by matching backtick RUN LENGTHS, with
// no backslash-escape processing inside it). The "\`" this function inserts
// is therefore COSMETICALLY inert at a call site that wraps the value in
// its own backticks (e.g. render.go's "- [`%s`](%s.md)\n" dependency links)
// -- harmless, not a structural injection, because every OTHER defence here
// still applies regardless: newlines are still collapsed, AND (as of the
// Finding 2 fix below) "[" and "]" are ALSO escaped by THIS function
// directly, so "](" can never be manufactured even if the code span itself
// renders oddly. Kept for defence-in-depth against non-code-span call sites
// (a Name rendered as plain "- **Name:** %s" prose, for instance, where
// "\`" DOES correctly prevent an unbalanced inline code span from opening).
//
// Bracket escaping was PREVIOUSLY documented here as deliberately omitted
// ("'[' and ']' carry no special meaning in plain heading/list-item prose,
// only within \"[text](url)\" link syntax") -- that claim was FALSE (Finding
// 2, round 5, 2026-07-16): GFM/CommonMark recognize "[text](url)" inline-link
// syntax in EVERY inline context, including plain heading and list-item
// prose that is not, on its own, "acting as" a link anywhere else on the
// page -- a free-text value can supply the WHOLE "[text](url)" construct by
// itself. A Title/Name such as "[Download update](https://evil.example)"
// contained no character the pre-fix version of this function escaped, so it
// passed through byte-for-byte into the skill detail page's H1 and
// "- **Title:**"/"- **Name:**" lines and rendered as a LIVE hyperlink with
// fully attacker-chosen link text on a page this generator itself controls.
// Escaping "[" and "]" directly in this function (rather than only in the
// escapeMDLinkText wrapper below, which existed for a NARROWER set of
// call sites) closes that gap at every escapeMDInline call site at once.
//
// Honest boundary (§11.4.6): this closes attacker-CHOSEN link TEXT only.
// GFM's extended autolinking (turning a BARE "https://..." URL appearing in
// prose into a live link with no "[...]" syntax at all) is unaffected by any
// escaping this function does or could do -- there is no "[" or "]" in a
// bare autolinked URL for this defence to intercept. That residual case is
// out of this fix's scope (this generator never claims to suppress GFM
// autolinking of a bare URL in free text it renders verbatim).
func escapeMDInline(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "*", `\*`)
	s = strings.ReplaceAll(s, "_", `\_`)
	s = strings.ReplaceAll(s, "~", `\~`)
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "[", `\[`)
	s = strings.ReplaceAll(s, "]", `\]`)
	if strings.HasPrefix(s, "#") {
		s = `\` + s
	}
	return s
}

// escapeMDLinkText is now a THIN ALIAS of escapeMDInline (Finding 2 fix,
// round 5, 2026-07-16): '['/']' escaping -- the reason this function used to
// exist as a distinct wrapper -- moved INTO escapeMDInline itself (see its
// doc comment above), so every escapeMDInline call site is now ALSO safe as
// a link's visible text; there is no longer any additional protection this
// function adds. It is kept as a separate, distinctly-named function purely
// for call-site readability/intent-documentation (a caller writing
// escapeMDLinkText(dep.Name) states "this is a link's visible text" even
// though, post-fix, escapeMDInline(dep.Name) would produce the byte-identical
// result) -- never used for a link's URL portion, which is always the output
// of slugify and already strips every non-filename-safe character.
func escapeMDLinkText(s string) string {
	return escapeMDInline(s)
}

// ---------------------------------------------------------------------------
// README.md (top-level index, DESIGN.md §2.1)
// ---------------------------------------------------------------------------

func renderREADME(records []skillRecord, fingerprint string) string {
	var b strings.Builder
	b.WriteString("# Skills Catalog\n\n")
	b.WriteString(generatedBanner)
	b.WriteString("\n")

	fmt.Fprintf(&b, "Total skills: %d\n\n", len(records))

	b.WriteString("## By Kind\n\n")
	kindCounts := countByKind(records)
	for _, k := range skillKindOrder {
		fmt.Fprintf(&b, "- %s: %d (see [by-kind/%s.md](by-kind/%s.md))\n", string(k), kindCounts[k], k, k)
	}
	b.WriteString("\n")

	b.WriteString("## By Status\n\n")
	statusCounts := countByStatus(records)
	for _, st := range skillStatusOrder {
		fmt.Fprintf(&b, "- %s: %d\n", string(st), statusCounts[st])
	}
	// unknownCount is the F7 review-finding remediation, round 2
	// (2026-07-16): sum every counted Status value NOT in the closed
	// skillStatusOrder set into an explicit "(unknown)" bucket, so this
	// breakdown ALWAYS sums to "Total skills" above -- see
	// unknownStatusLabel's doc comment for why this is the chosen fix (vs.
	// failing closed in loadRoster) and for the captured evidence that a
	// literal SQL NULL status is unreachable through this path without
	// erroring first.
	unknownCount := 0
	for st, n := range statusCounts {
		if !knownSkillStatus(st) {
			unknownCount += n
		}
	}
	if unknownCount > 0 {
		fmt.Fprintf(&b, "- %s: %d\n", unknownStatusLabel, unknownCount)
	}
	b.WriteString("\n")

	b.WriteString("## By Domain\n\n")
	domains, unclassifiedCount := domainCounts(records)
	for _, d := range domains {
		fmt.Fprintf(&b, "- [%s](by-domain/%s.md): %d skill(s)\n", escapeMDLinkText(d.name), d.slug, d.count)
	}
	if unclassifiedCount > 0 {
		fmt.Fprintf(&b, "- [Unclassified](by-domain/_unclassified.md): %d skill(s)\n", unclassifiedCount)
	}
	b.WriteString("\n")

	b.WriteString("## Full Index\n\n")
	b.WriteString("See [INDEX.md](INDEX.md) for the full flat table of every skill.\n\n")

	// Deliberately NO wall-clock "Last generated" timestamp ANYWHERE in this
	// package's generated output -- not just here in README.md (a genuine,
	// documented departure from DESIGN.md §2.1's literal text -- see this
	// PWU's final report for the §11.4.6 discrepancy note): embedding any
	// wall-clock value in rendered CONTENT would break the byte-stability
	// contract this generator's own tests enforce ("identical DB state =>
	// byte-identical output"). The per-skill detail page's Footer
	// (renderSkillDetail, below) deliberately omits Created/Updated for the
	// SAME reason (F1 review-finding remediation, round 2, 2026-07-16) --
	// those columns are real churn metadata (an idempotent re-import bumps
	// updated_at with no catalog-visible change) that must never leak into
	// a deterministic generated artifact. The roster fingerprint below is
	// the sole, deterministic freshness signal for THIS file: it changes if
	// and only if the catalog-visible roster actually changed.
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "Roster fingerprint: `%s` (generator `%s`)\n", fingerprint, GeneratorVersion)

	return b.String()
}

type domainCount struct {
	name  string
	slug  string
	count int
}

func domainCounts(records []skillRecord) ([]domainCount, int) {
	counts := make(map[string]int)
	unclassified := 0
	for _, r := range records {
		if r.Metadata.Domain == "" {
			unclassified++
			continue
		}
		counts[r.Metadata.Domain]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]domainCount, 0, len(names))
	for _, name := range names {
		out = append(out, domainCount{name: name, slug: slugify(name), count: counts[name]})
	}
	return out, unclassified
}

func countByKind(records []skillRecord) map[models.SkillKind]int {
	m := map[models.SkillKind]int{}
	for _, r := range records {
		m[r.Skill.Kind.NormalizeOrAtomic()]++
	}
	return m
}

func countByStatus(records []skillRecord) map[models.SkillStatus]int {
	m := map[models.SkillStatus]int{}
	for _, r := range records {
		m[r.Skill.Status]++
	}
	return m
}

// unknownStatusLabel is the "By Status" breakdown bucket for a
// skill.Status value outside skillStatusOrder's four known values (model.go)
// (F7 review finding, round 2, 2026-07-16).
//
// DESIGN CHOICE (documented per the finding's own instruction to "pick one
// [fix] and document the choice"): this generator adds an explicit
// "(unknown)" bucket rather than failing closed in loadRoster, because a
// LITERAL SQL NULL status -- the only way to defeat the `status ... CHECK
// (status IN (...))` constraint (migrations/001_initial.up.sql:17), since
// Postgres CHECK constraints treat NULL as satisfying the check unless
// NOT NULL is also declared -- is EMPIRICALLY UNREACHABLE through this
// package's own read path without erroring first: Store.ListSkills/
// Store.GetByName scan the column directly into the non-nullable
// models.SkillStatus (string) field, and pgx v5 hard-errors ("cannot scan
// NULL into *models.SkillStatus: cannot scan NULL into *string") before
// loadRoster ever sees the row. Captured evidence: a throwaway-DB probe
// (`UPDATE skills SET status = NULL ...` then `store.ListSkills`/
// `Generate`) reproduced exactly that scan error on every call site,
// wrapped by loadRoster's own "list skills: %w" -- i.e. the package ALREADY
// fails closed for genuine NULL, just with an unfriendly driver error
// rather than an ErrDefensiveCheck-wrapped one; adding a SECOND, redundant
// Go-level NULL check in loadRoster would be dead code the scan error makes
// unreachable (see §11.4.124 -- unreachable code must be proven unreachable
// before being justified, which this comment does).
//
// The REAL, currently-latent version of the F7 concern is a status value
// that is NOT NULL but also not one of the four SkillStatus constants this
// generator's skillStatusOrder (model.go) enumerates -- e.g. a FIFTH value
// added to the DB's CHECK constraint by a future migration before this
// generator is updated to match. That case scans successfully (it is a
// perfectly valid non-NULL string) and previously reached renderREADME's
// "By Status" loop, which iterates ONLY the four skillStatusOrder values --
// silently omitting any skill in that fifth bucket from the breakdown while
// "Total skills" still counted it. The "(unknown)" bucket closes that gap:
// every record's Status falls into exactly one of the four known buckets or
// this one, so the breakdown always sums to the total.
const unknownStatusLabel = "(unknown)"

// knownSkillStatus reports whether st is one of the four closed-set values
// skillStatusOrder (model.go) enumerates.
func knownSkillStatus(st models.SkillStatus) bool {
	for _, k := range skillStatusOrder {
		if k == st {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// INDEX.md (full flat table, DESIGN.md §2.2)
// ---------------------------------------------------------------------------

func renderIndex(records []skillRecord) string {
	var b strings.Builder
	b.WriteString("# Skills Index\n\n")
	b.WriteString(generatedBanner)
	b.WriteString("\n")
	b.WriteString("| Name | Kind | Status | Domain | Complexity | Version | #Deps | #Resources | Link |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range records {
		domain := r.Metadata.Domain
		if domain == "" {
			domain = "_(unclassified)_"
		}
		depCount := 0
		for _, list := range r.DepsByType {
			depCount += len(list)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %d | %d | [link](skill/%s.md) |\n",
			escapeMDCell(r.Skill.Name), r.Skill.Kind.NormalizeOrAtomic(), r.Skill.Status, escapeMDCell(domain),
			escapeMDCell(r.Metadata.Complexity), escapeMDCell(r.Skill.Version), depCount, len(r.Resources), r.NameSlug)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// by-domain/<slug>.md and by-kind/<kind>.md (category groupings, DESIGN.md §2.3)
// ---------------------------------------------------------------------------

func renderGroupingPage(title string, records []skillRecord, emptyNote string) string {
	var b strings.Builder
	// title embeds free-text renderDomainPage's Metadata.Domain for a
	// by-domain page (renderKindPage's models.SkillKind is a controlled
	// enum and is unaffected by escaping) -- escapeMDInline here covers
	// both callers uniformly (F3 review finding, round 2, 2026-07-16).
	fmt.Fprintf(&b, "# %s\n\n", escapeMDInline(title))
	b.WriteString(generatedBanner)
	b.WriteString("\n")
	if len(records) == 0 {
		fmt.Fprintf(&b, "_%s_\n", emptyNote)
		return b.String()
	}
	b.WriteString("| Name | Title | Status | Link |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, r := range records {
		fmt.Fprintf(&b, "| %s | %s | %s | [link](../skill/%s.md) |\n",
			escapeMDCell(r.Skill.Name), escapeMDCell(r.Skill.Title), r.Skill.Status, r.NameSlug)
	}
	return b.String()
}

func renderDomainPage(domainName string, records []skillRecord) string {
	return renderGroupingPage(fmt.Sprintf("Domain: %s", domainName), records, "No skills in this domain.")
}

func renderKindPage(kind models.SkillKind, records []skillRecord) string {
	return renderGroupingPage(fmt.Sprintf("Kind: %s", kind), records, "No skills of this kind yet.")
}

// ---------------------------------------------------------------------------
// skill/<name>.md (per-skill detail page, DESIGN.md §2.4 -- the atomic
// generated unit)
// ---------------------------------------------------------------------------

// sectionBegin/sectionEnd wrap every generator-emitted section of the
// per-skill detail page in a stable, machine-greppable HTML comment marker
// (F-B review finding, round 3, 2026-07-16). sk.Description and sk.Content
// are rendered VERBATIM below -- by design, they are real skill body
// content, never run through escapeMDInline/escapeMDCell like every other
// free-text field on this page (see the "## Content"/"## Description"
// comments below for why escaping them would be WRONG, not merely
// incomplete) -- so an ingested skill whose Description contains its own
// "## Footer\n\n- _Generated by skills-catalog/v2 from roster fingerprint
// deadbeef._" text renders a forged section heading and a forged footer
// line that are, by Markdown structure ALONE, indistinguishable from an
// authentic generator-emitted "## Footer" section.
//
// These sentinels give any downstream consumer that keys off them --
// rather than naively grepping the rendered file for "## Footer" or
// "## <SectionName>" -- an unforgeable boundary: content between a
// `section=X` BEGIN marker and ITS matching END marker is authentic
// generator structure for section X; a "## Footer" heading appearing
// anywhere else -- in particular, INSIDE another section's sentinel-
// bounded body, such as the Description section's own bounded region --
// is necessarily someone's raw prose, never mistaken for the genuine
// article by a consumer that tracks which section is currently open. The
// markers do not change what a human reader of the rendered Markdown sees
// (HTML comments render as invisible in every standard Markdown renderer,
// GitHub/GitLab/CommonMark included) -- they exist purely as an
// unforgeable machine boundary, proven by
// TestSkillsCatalog_ForgedSectionHeading_SentinelDistinguishesAuthenticFooter
// (generate_test.go): a Description containing a forged, sentinel-less
// "## Footer" heading + a fake fingerprint renders TWO "## Footer"
// substrings, but only ONE is wrapped in the real `section=footer`
// sentinel pair, and that one carries the REAL fingerprint.
//
// Honest boundary (§11.4.6): these sentinels defend against a forged
// PROSE HEADING masquerading as generator structure -- the threat this
// finding describes and the test above reproduces. They do NOT defend
// against a hypothetically MORE sophisticated forgery that embeds the
// exact literal sentinel comment text itself inside Description/Content;
// closing that residual case would need a cryptographically unforgeable
// marker (e.g. an HMAC over the section body) rather than a fixed string,
// which is out of this finding's scope.
//
// R3-R2 review finding (round 4, 2026-07-16, comment-accuracy-only): the
// previous version of this paragraph additionally claimed a parser that
// "correctly tracks section NESTING" still resolves even a forged-sentinel
// attack correctly. FALSE, proven: a section-NESTING tracker is
// defeatable by a close-first/reopen-after attack. An adversarial
// Description can embed the literal bytes `<!-- /skills-catalog:section=
// description -->` (closing the currently-open description section
// EARLY, from the tracker's point of view), followed by a forged, fully
// well-formed top-level `<!-- skills-catalog:section=footer -->` ... `<!--
// /skills-catalog:section=footer -->` pair containing a fake fingerprint,
// followed by re-opening `<!-- skills-catalog:section=description -->` so
// the REAL description section (as the generator actually emitted it)
// still balances its own real BEGIN/END pair. The resulting byte stream is
// PERFECTLY nested throughout -- every BEGIN has a matching END, at every
// depth -- so a parser whose only defence is nesting-depth tracking has no
// signal to reject the forged footer: it is textually indistinguishable
// from a second, legitimately-standalone top-level section. Only
// duplicate-section detection (reject a second `section=footer` BEGIN
// after the first has already closed), canonical-order validation (this
// generator always emits header/description/dependencies/dependents/
// resources/content/footer in that fixed order -- any other order is
// forged), a take-the-LAST-footer-only rule, or a cryptographic marker
// (e.g. an HMAC over the section body, as the paragraph above already
// notes) actually resolves this attack; nesting-tracking alone does not.
// This out-of-scope disclosure is otherwise unchanged: closing the attack
// is still deliberately left to a downstream consumer per this finding's
// scope, but the guidance above no longer tells that consumer a
// nesting-tracking parser would already be safe.
func sectionBegin(b *strings.Builder, name string) {
	fmt.Fprintf(b, "<!-- skills-catalog:section=%s -->\n", name)
}

func sectionEnd(b *strings.Builder, name string) {
	fmt.Fprintf(b, "<!-- /skills-catalog:section=%s -->\n", name)
}

func renderSkillDetail(r skillRecord, cfg Config, fingerprintPrefix string) string {
	sk := r.Skill
	var b strings.Builder

	// sk.Name is escaped via escapeMDInline here AND in the "- **Name:**"
	// list item below (F3 review finding, round 2, 2026-07-16): a Name
	// containing an embedded newline/backtick/asterisk/underscore/leading
	// '#' is legal input (model.go's slugify docstring -- skills.name has
	// no charset constraint beyond TEXT UNIQUE) and must not be able to
	// manufacture a bogus heading or corrupt inline formatting.
	fmt.Fprintf(&b, "# %s\n\n", escapeMDInline(sk.Name))
	b.WriteString(generatedBanner)
	b.WriteString("\n")

	// 1. Header. Kind/Status are rendered UNESCAPED -- both are
	// controlled, closed-set enum values (models.SkillKind/SkillStatus),
	// never free text, so they carry no injection risk (F3 review finding,
	// round 2, 2026-07-16 -- only genuinely free-text fields need
	// escapeMDInline).
	sectionBegin(&b, "header")
	b.WriteString("## Header\n\n")
	fmt.Fprintf(&b, "- **Name:** %s\n", escapeMDInline(sk.Name))
	fmt.Fprintf(&b, "- **Title:** %s\n", escapeMDInline(sk.Title))
	fmt.Fprintf(&b, "- **Version:** %s\n", escapeMDInline(sk.Version))
	fmt.Fprintf(&b, "- **Kind:** %s\n", sk.Kind.NormalizeOrAtomic())
	fmt.Fprintf(&b, "- **Status:** %s\n", sk.Status)
	domain := r.Metadata.Domain
	if domain == "" {
		b.WriteString("- **Domain:** _(unclassified)_\n")
	} else {
		fmt.Fprintf(&b, "- **Domain:** %s\n", escapeMDInline(domain))
	}
	fmt.Fprintf(&b, "- **Complexity:** %s\n", escapeMDInline(r.Metadata.Complexity))
	tags := append([]string(nil), r.Metadata.Tags...)
	sort.Strings(tags)
	escapedTags := make([]string, len(tags))
	for i, tg := range tags {
		escapedTags[i] = escapeMDInline(tg)
	}
	fmt.Fprintf(&b, "- **Tags:** %s\n", strings.Join(escapedTags, ", "))
	b.WriteString("\n")
	sectionEnd(&b, "header")

	// 2. Description
	sectionBegin(&b, "description")
	b.WriteString("## Description\n\n")
	fmt.Fprintf(&b, "%s\n\n", sk.Description)
	sectionEnd(&b, "description")

	// 3. Dependencies (relation-type subsections; zero-entry types omitted)
	depsSection := renderDependenciesSection(r)
	if depsSection != "" {
		sectionBegin(&b, "dependencies")
		b.WriteString("## Dependencies\n\n")
		b.WriteString(depsSection)
		sectionEnd(&b, "dependencies")
	}

	// 4. Dependents (reverse edges; omitted entirely when zero, mirroring
	// the Dependencies section's own "never an empty heading" rule)
	if len(r.Dependents) > 0 {
		sectionBegin(&b, "dependents")
		b.WriteString("## Dependents\n\n")
		for _, dep := range r.Dependents {
			// dep.Name is escaped as LINK TEXT (F3 review finding, round 2,
			// 2026-07-16): it is both the visible text between backticks AND
			// inside "[...]" link syntax, so a Name containing a backtick or
			// "]"/"[" must not corrupt either. The URL portion (slugify(dep.Name))
			// is already filesystem-sanitized and needs no further escaping.
			fmt.Fprintf(&b, "- [`%s`](%s.md)\n", escapeMDLinkText(dep.Name), slugify(dep.Name))
		}
		b.WriteString("\n")
		sectionEnd(&b, "dependents")
	}

	// 5. Resources
	if len(r.Resources) > 0 {
		sectionBegin(&b, "resources")
		b.WriteString("## Resources\n\n")
		b.WriteString("| Title | URL | Type |\n")
		b.WriteString("|---|---|---|\n")
		for _, res := range r.Resources {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", escapeMDCell(res.Title), escapeMDCell(res.URL), escapeMDCell(res.ResourceType))
		}
		b.WriteString("\n")
		sectionEnd(&b, "resources")
	}

	// 6. Content (EmbedFullContent toggle, DESIGN.md §2.4 item 6/§7 item 3)
	sectionBegin(&b, "content")
	b.WriteString("## Content\n\n")
	b.WriteString("---\n\n")
	if cfg.EmbedFullContent {
		if sk.Content == "" {
			b.WriteString("_(empty)_\n\n")
		} else {
			b.WriteString(sk.Content)
			if !strings.HasSuffix(sk.Content, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	} else {
		excerpt := contentExcerpt(sk.Content)
		if excerpt == "" {
			b.WriteString("_(empty)_\n\n")
		} else {
			// sk.Name is escaped here too (F3 review finding, round 2,
			// 2026-07-16): it sits inside a backtick-delimited inline code
			// span ("`skill-system skill export %s`"), so an embedded
			// backtick would break out of it. sk.ID is a machine-generated
			// UUID (hex digits + hyphens only) and structurally cannot
			// contain a Markdown-significant character, so it needs no
			// escaping.
			fmt.Fprintf(&b, "%s\n\n_Full content omitted (EmbedFullContent=false); use `skill-system skill export %s` "+
				"or `GET /skills/%s/export` for the complete body._\n\n", excerpt, escapeMDInline(sk.Name), sk.ID)
		}
	}
	sectionEnd(&b, "content")

	// 8. Footer (item 7 "Source" -- seed TOML link -- is an OPTIONAL,
	// explicitly out-of-scope-for-v1 feature per this PWU's report; omitted
	// here rather than guessed at, §11.4.6).
	//
	// Created/Updated timestamps are DELIBERATELY OMITTED (F1 review
	// finding, round 2, 2026-07-16): they are real CHURN metadata --
	// created_at/updated_at are EXCLUDED from the roster fingerprint
	// (fingerprint.go's computeRosterFingerprint docstring) precisely
	// because an idempotent re-import bumps updated_at with NO
	// catalog-visible change (internal/skill/store.go's Create upsert path
	// + migrations/001_initial.up.sql's BEFORE-UPDATE trigger) -- rendering
	// them here made that exact touch-only mutation leave Verify/Generate
	// reporting "in sync"/"not regenerated" while this Footer showed a
	// now-stale timestamp (the F1 staleness bug this round closes for good:
	// a churn field must be excluded from BOTH the fingerprint AND the
	// rendered output, never just one). Freshness is EXCLUSIVELY signalled
	// by the roster fingerprint below -- deterministic, and unaffected by
	// any row's created_at/updated_at.
	sectionBegin(&b, "footer")
	b.WriteString("## Footer\n\n")
	fmt.Fprintf(&b, "- _Generated by `%s` from roster fingerprint `%s`._\n", GeneratorVersion, fingerprintPrefix)
	sectionEnd(&b, "footer")

	return b.String()
}

func renderDependenciesSection(r skillRecord) string {
	var b strings.Builder
	for _, relType := range canonicalRelationOrder {
		list := r.DepsByType[relType]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", relationTypeLabel[relType])
		for _, dep := range list {
			var attrs []string
			if dep.Optional {
				attrs = append(attrs, "optional")
			}
			if dep.SortOrder != nil {
				attrs = append(attrs, fmt.Sprintf("sort_order=%d", *dep.SortOrder))
			}
			// dep.DependsOnName is escaped as LINK TEXT for the same reason
			// as the Dependents section above (F3 review finding, round 2,
			// 2026-07-16).
			if len(attrs) > 0 {
				fmt.Fprintf(&b, "- [`%s`](%s.md) _(%s)_\n", escapeMDLinkText(dep.DependsOnName), slugify(dep.DependsOnName), strings.Join(attrs, ", "))
			} else {
				fmt.Fprintf(&b, "- [`%s`](%s.md)\n", escapeMDLinkText(dep.DependsOnName), slugify(dep.DependsOnName))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func contentExcerpt(content string) string {
	const maxLen = 240
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	r := []rune(trimmed)
	if len(r) <= maxLen {
		return trimmed
	}
	return string(r[:maxLen]) + "…"
}
