package skillscatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
)

// Config controls optional generator behaviour. The zero Config is NOT the
// intended default -- callers should use DefaultConfig() (EmbedFullContent
// defaults to true per DESIGN.md §2.4 item 6 / §7 item 3).
type Config struct {
	// EmbedFullContent, when true (the default), embeds each skill's full
	// Content field verbatim on its detail page. When false, a short
	// excerpt is rendered instead with a pointer to the live export
	// surfaces (DESIGN.md §2.4 item 6 -- a named, documented toggle, never
	// a silently-hardcoded choice).
	EmbedFullContent bool

	// Force, when true, bypasses the fingerprint short-circuit and
	// rewrites the tree even when the roster fingerprint is unchanged.
	// Used by callers (this package's own determinism tests; an eventual
	// CLI --force flag, G126+) that need to prove byte-for-byte
	// regeneration stability rather than merely observe the short-circuit
	// taking effect (DESIGN.md §3.2).
	Force bool

	// MaxRosterRows bounds the single Store.ListSkills call loadRoster
	// makes in place of an "unbounded" rows count (F-E review finding,
	// round 3, 2026-07-16 -- promoted out of the former package-private
	// `listAllLimit` var; see load.go's defaultMaxRosterRows doc comment
	// for the full rationale). Zero (the zero Config's own value) means
	// "use the recommended default" -- DefaultConfig sets this explicitly
	// to defaultMaxRosterRows so callers never need to know that number.
	MaxRosterRows int
}

// DefaultConfig returns the recommended default configuration.
func DefaultConfig() Config {
	return Config{EmbedFullContent: true, MaxRosterRows: defaultMaxRosterRows}
}

// Generate reads every skill (+ its dependencies, dependents, and
// resources) from store via the existing Store read methods, computes the
// §11.4.86 roster fingerprint, and -- ONLY if the composite sidecar
// identity (fingerprint.go's computeSidecarIdentity: GeneratorVersion +
// cfg's output-affecting fields + the roster fingerprint) differs from the
// one recorded at outputDir/.catalog_fingerprint, or cfg.Force is set --
// deterministically (re)writes the full docs/skills/**-shaped tree under
// outputDir.
//
// Returns whether a regeneration actually happened (false = the composite
// identity is unchanged and cfg.Force is false: a genuine no-op, not a
// write that silently happened anyway) and the current ROSTER fingerprint
// (not the composite identity -- see computeSidecarIdentity's doc comment
// for why these two are deliberately kept distinct) either way, so callers
// can report "already up to date" honestly rather than claim a write that
// did not happen.
func Generate(ctx context.Context, store *skill.Store, outputDir string, cfg Config) (regenerated bool, fingerprint string, err error) {
	records, err := loadRoster(ctx, store, cfg)
	if err != nil {
		return false, "", err
	}

	rosterFingerprint := computeRosterFingerprint(records)
	identity := computeSidecarIdentity(cfg, rosterFingerprint)

	recorded, exists, err := readFingerprintSidecar(outputDir)
	if err != nil {
		return false, "", err
	}
	if exists && recorded == identity && !cfg.Force {
		return false, rosterFingerprint, nil
	}

	if err := writeTree(outputDir, records, cfg, rosterFingerprint); err != nil {
		return false, "", err
	}
	if err := writeFingerprintSidecar(outputDir, identity); err != nil {
		return false, "", err
	}

	return true, rosterFingerprint, nil
}

// Verify recomputes the roster fingerprint and the composite sidecar
// identity (fingerprint.go's computeSidecarIdentity, keyed off cfg -- F6
// review finding, round 2, 2026-07-16) and compares the identity to the
// sidecar WITHOUT writing anything -- the read-only drift check a
// pre-commit/CI gate (G126+, explicitly out of this PWU's scope) will call,
// answering "if I ran Generate(ctx, store, outputDir, cfg) right now, would
// it be a no-op?". cfg MUST be the SAME configuration the caller intends to
// (or most recently did) call Generate with -- Verify has no way to know
// this on its own, so it is an explicit parameter rather than an assumed
// default; passing a DIFFERENT cfg than what actually produced the on-disk
// tree correctly reports inSync=false (the tree does not match what THAT
// cfg would produce), which is the desired behaviour, not a bug.
//
// inSync is false (never an error) when outputDir has no sidecar yet.
func Verify(ctx context.Context, store *skill.Store, outputDir string, cfg Config) (inSync bool, currentFingerprint, recordedFingerprint string, err error) {
	records, err := loadRoster(ctx, store, cfg)
	if err != nil {
		return false, "", "", err
	}
	currentFingerprint = computeRosterFingerprint(records)
	currentIdentity := computeSidecarIdentity(cfg, currentFingerprint)

	recordedIdentity, exists, err := readFingerprintSidecar(outputDir)
	if err != nil {
		return false, currentFingerprint, "", err
	}
	if !exists {
		return false, currentFingerprint, "", nil
	}
	return recordedIdentity == currentIdentity, currentFingerprint, recordedIdentity, nil
}

func writeTree(outputDir string, records []skillRecord, cfg Config, fingerprint string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	fpPrefix := fingerprint
	if len(fpPrefix) > 12 {
		fpPrefix = fpPrefix[:12]
	}

	if err := writeFile(filepath.Join(outputDir, "README.md"), renderREADME(records, fingerprint)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outputDir, "INDEX.md"), renderIndex(records)); err != nil {
		return err
	}
	if err := writeByDomain(outputDir, records); err != nil {
		return err
	}
	if err := writeByKind(outputDir, records); err != nil {
		return err
	}
	if err := writeSkillPages(outputDir, records, cfg, fpPrefix); err != nil {
		return err
	}

	return nil
}

func writeByDomain(outputDir string, records []skillRecord) error {
	dir := filepath.Join(outputDir, "by-domain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create by-domain dir: %w", err)
	}
	// Clear stale per-domain pages from a previous generation whose domain
	// set has since shrunk -- the tree must always reflect EXACTLY the
	// current roster, never a superset of it (a domain with zero remaining
	// members must not leave a dangling stale page behind).
	if err := clearGeneratedMarkdown(dir); err != nil {
		return err
	}

	grouped := make(map[string][]skillRecord) // domain slug -> records
	domainTitle := make(map[string]string)    // domain slug -> real domain name
	var unclassified []skillRecord

	for _, r := range records {
		if r.Metadata.Domain == "" {
			unclassified = append(unclassified, r)
			continue
		}
		grouped[r.DomainSlug] = append(grouped[r.DomainSlug], r)
		domainTitle[r.DomainSlug] = r.Metadata.Domain
	}

	slugs := make([]string, 0, len(grouped))
	for s := range grouped {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)

	for _, s := range slugs {
		if err := writeFile(filepath.Join(dir, s+".md"), renderDomainPage(domainTitle[s], grouped[s])); err != nil {
			return err
		}
	}
	if len(unclassified) > 0 {
		if err := writeFile(filepath.Join(dir, "_unclassified.md"), renderDomainPage("Unclassified", unclassified)); err != nil {
			return err
		}
	}
	return nil
}

func writeByKind(outputDir string, records []skillRecord) error {
	dir := filepath.Join(outputDir, "by-kind")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create by-kind dir: %w", err)
	}

	grouped := map[models.SkillKind][]skillRecord{}
	for _, r := range records {
		k := r.Skill.Kind.NormalizeOrAtomic()
		grouped[k] = append(grouped[k], r)
	}

	// skillKindOrder is a FIXED, closed three-value set (unlike by-domain's
	// data-driven pages): all three files are always (re)written, even when
	// a bucket is currently empty (DESIGN.md §2.3).
	for _, k := range skillKindOrder {
		if err := writeFile(filepath.Join(dir, string(k)+".md"), renderKindPage(k, grouped[k])); err != nil {
			return err
		}
	}
	return nil
}

func writeSkillPages(outputDir string, records []skillRecord, cfg Config, fpPrefix string) error {
	dir := filepath.Join(outputDir, "skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}
	if err := clearGeneratedMarkdown(dir); err != nil {
		return err
	}

	for _, r := range records {
		if err := writeFile(filepath.Join(dir, r.NameSlug+".md"), renderSkillDetail(r, cfg, fpPrefix)); err != nil {
			return err
		}
	}
	return nil
}

// clearGeneratedMarkdown removes every *.md file directly inside dir before
// a fresh write pass, so a skill/domain that no longer exists does not leave
// a stale generated page behind. Only *.md files are removed -- this
// function is only ever pointed at directories this package itself
// exclusively owns and populates (by-domain/, skill/).
func clearGeneratedMarkdown(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("remove stale generated file %s: %w", e.Name(), err)
		}
	}
	return nil
}

func writeFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
