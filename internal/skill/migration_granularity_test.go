package skill

// M6 + M10 of the P1.T1 granularity-schema migration-acceptance suite
// (research/p1t1_granularity_schema_migration.md §4.1). M1-M5/M7-M9 (pure
// schema/migration-mechanics cases needing only *db.Pool) live in
// internal/db/migrations_granularity_test.go; M6 (kind/optional/sort_order
// round-trip through skill.Store) and M10 (seed TOML import + validate_dag.py)
// need the Store layer this package owns, hence live here.
//
// Gated on the same SKILL_SYSTEM_TEST_DB_* environment contract as the
// internal/db suite (see that package's testdb_helper_test.go for the full
// rationale) -- absent a configured live PostgreSQL, both cases honestly
// t.Skip() (§11.4.3/§11.4.27), matching this package's pre-existing
// _RequiresLiveDatabase idiom (graph_test.go).

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/jackc/pgx/v5"
)

const realMigrationsDirFromSkillPkg = "../../migrations"
const realSeedDirFromSkillPkg = "../../seed"

func skillTestDBAdminConfig() (config.DatabaseConfig, bool) {
	host := os.Getenv("SKILL_SYSTEM_TEST_DB_HOST")
	if host == "" {
		return config.DatabaseConfig{}, false
	}
	port := 5432
	if p := os.Getenv("SKILL_SYSTEM_TEST_DB_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	user := os.Getenv("SKILL_SYSTEM_TEST_DB_USER")
	if user == "" {
		user = "postgres"
	}
	adminDB := os.Getenv("SKILL_SYSTEM_TEST_DB_ADMIN_DB")
	if adminDB == "" {
		adminDB = "postgres"
	}
	return config.DatabaseConfig{
		Host:           host,
		Port:           port,
		Database:       adminDB,
		User:           user,
		Password:       os.Getenv("SKILL_SYSTEM_TEST_DB_PASSWORD"),
		SSLMode:        "disable",
		MaxConnections: 4,
		ConnectTimeout: 10 * time.Second,
	}, true
}

func skillSkipIfNoTestDB(t *testing.T) (config.DatabaseConfig, bool) {
	t.Helper()
	admin, ok := skillTestDBAdminConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance (research/" +
			"p1t1_granularity_schema_migration.md §5.3 honest gap; same " +
			"boundary as this package's other _RequiresLiveDatabase tests).")
		return config.DatabaseConfig{}, false
	}
	return admin, true
}

func skillCreateThrowawayDB(t *testing.T, admin config.DatabaseConfig) (config.DatabaseConfig, func()) {
	t.Helper()
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, admin.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to admin database %q: %v", admin.Database, err)
	}
	defer adminConn.Close(ctx)

	n, rerr := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if rerr == nil {
		suffix = n.String()
	}
	dbName := "skillsys_test_" + suffix

	if _, err := adminConn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Fatalf("create throwaway database %q: %v", dbName, err)
	}

	dbCfg := admin
	dbCfg.Database = dbName

	extConn, err := pgx.Connect(ctx, dbCfg.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to throwaway database %q: %v", dbName, err)
	}
	if _, err := extConn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS "uuid-ossp";`); err != nil {
		extConn.Close(ctx)
		t.Fatalf("create extensions in throwaway database %q: %v", dbName, err)
	}
	extConn.Close(ctx)

	cleanup := func() {
		cctx := context.Background()
		c, err := pgx.Connect(cctx, admin.DSNWithTimeout())
		if err != nil {
			return
		}
		defer c.Close(cctx)
		_, _ = c.Exec(cctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		_, _ = c.Exec(cctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize()))
	}

	return dbCfg, cleanup
}

// M6 — kind/optional/sort_order round-trip: create a skill with
// Kind=SkillKindUmbrella and a dependency edge with Optional=true,
// SortOrder=3, then read it back via GetByName and assert every new field is
// preserved identically.
func TestP1T1Migration_M6_KindAndEdgeAttributesRoundTripThroughStore(t *testing.T) {
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := skillCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, realMigrationsDirFromSkillPkg); err != nil {
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}

	store := NewStore(pool)

	dependency := &models.Skill{
		Name:    "p1t1.m6.component",
		Title:   "Component",
		Content: "component content",
		Status:  models.SkillStatusDraft,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, dependency); err != nil {
		t.Fatalf("create component skill: %v", err)
	}

	sortOrder := 3
	umbrella := &models.Skill{
		Name:    "p1t1.m6.umbrella",
		Title:   "Umbrella",
		Content: "umbrella content",
		Status:  models.SkillStatusDraft,
		Kind:    models.SkillKindUmbrella,
		Dependencies: []models.SkillDependency{
			{
				DependsOn:    dependency.ID,
				RelationType: models.DepTypeComposes,
				Optional:     true,
				SortOrder:    &sortOrder,
			},
		},
	}
	if err := store.Create(ctx, umbrella); err != nil {
		t.Fatalf("create umbrella skill with composes edge: %v", err)
	}

	got, err := store.GetByName(ctx, "p1t1.m6.umbrella")
	if err != nil {
		t.Fatalf("GetByName(p1t1.m6.umbrella): %v", err)
	}

	if got.Kind != models.SkillKindUmbrella {
		t.Errorf("round-tripped Kind = %q, want %q", got.Kind, models.SkillKindUmbrella)
	}
	if len(got.Dependencies) != 1 {
		t.Fatalf("round-tripped Dependencies count = %d, want 1", len(got.Dependencies))
	}
	dep := got.Dependencies[0]
	if dep.RelationType != models.DepTypeComposes {
		t.Errorf("round-tripped RelationType = %q, want %q", dep.RelationType, models.DepTypeComposes)
	}
	if !dep.Optional {
		t.Error("round-tripped Optional = false, want true")
	}
	if dep.SortOrder == nil || *dep.SortOrder != 3 {
		t.Errorf("round-tripped SortOrder = %v, want pointer to 3", dep.SortOrder)
	}

	// W3 fix (Fable code-review remediation): ExportToTOML's TOMLSkillDef
	// literal never set Kind, so an exported umbrella/composite skill would
	// silently re-import as 'atomic' (the column DEFAULT). Export the
	// umbrella skill just created and confirm skill.kind == "umbrella"
	// survives export. (Re-importing under a colliding name is avoided --
	// ImportFromTOML enforces unique skill.name -- so this asserts directly
	// against what ExportToTOML actually emits, decoded via the SAME
	// BurntSushi/toml + models.TOMLSkillWrapper the real import path uses.)
	exported, err := store.ExportToTOML(ctx, "p1t1.m6.umbrella")
	if err != nil {
		t.Fatalf("ExportToTOML(p1t1.m6.umbrella): %v", err)
	}
	var exportedWrapper models.TOMLSkillWrapper
	if err := toml.Unmarshal(exported, &exportedWrapper); err != nil {
		t.Fatalf("toml.Unmarshal exported TOML: %v\n--- exported TOML ---\n%s", err, exported)
	}
	if exportedWrapper.Skill.Kind != string(models.SkillKindUmbrella) {
		t.Errorf("exported skill.kind = %q, want %q (W3: ExportToTOML omitted Kind, so an exported umbrella/composite skill silently downgrades to 'atomic' on re-import)",
			exportedWrapper.Skill.Kind, models.SkillKindUmbrella)
	}
}

// M10 — seed TOMLs import as kind='atomic' (the column DEFAULT, since none
// of the seed files declare `kind`), and seed/validate_dag.py still reports
// the corpus a valid DAG under the new hard-closure-scoped acyclicity +
// granularity-invariant checks (L15).
//
// B1 fix consequence (real finding, not a defect introduced by this
// remediation): this test's ORIGINAL premise -- "all 8 seed TOMLs import
// successfully" -- was true ONLY because the pre-fix dotted-tag decode bug
// (see models.TOMLSkillWrapper's doc comment) made wrapper.Dependencies
// always decode empty, so ImportFromTOML's requires-resolution loop never
// ran and never enforced anything. With B1 fixed, requires enforcement is
// genuinely active for the first time, and 3 of the 8 seed TOML files
// declare a hard `requires` on a skill with NO seed/skills/*.toml file:
// cpp.language requires "c.language"; make.build_system requires
// "c.language"; android.aosp.build_system requires "python.language" (and
// "make.build_system", which is itself blocked transitively via
// cpp.language -> cmake.build_system). CORPUS.yaml documents c.language /
// python.language / bazel.build_system as declared-but-not-yet-authored
// corpus targets (`seed: false`), so this is a genuine, pre-existing
// content gap in the seed corpus (3 more seed/skills/*.toml files need
// authoring) -- separately actionable, but out of THIS remediation's scope
// (content authoring, not a code/test/migration fix). Exactly the 4 seed
// skills whose FULL requires-transitive-closure resolves within the
// existing 8-file corpus (linux.os, java.language, kotlin.language,
// android.overview) import successfully; the other 4 correctly fail closed
// with ErrDependencyNotFound, which this test now asserts explicitly rather
// than assuming (falsely, as the pre-fix bug masked) that every file
// imports.
func TestP1T1Migration_M10_SeedTOMLsStillLoadAsAtomicAndValidatorGreen(t *testing.T) {
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := skillCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, realMigrationsDirFromSkillPkg); err != nil {
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}

	store := NewStore(pool)

	seedSkillsDir := filepath.Join(realSeedDirFromSkillPkg, "skills")
	entries, err := os.ReadDir(seedSkillsDir)
	if err != nil {
		t.Fatalf("read seed skills dir %q: %v", seedSkillsDir, err)
	}

	// Import in two passes: skills with zero requires/extends first is not
	// guaranteed by filesystem order, so retry until every file has resolved
	// (bounded by len(entries) passes -- the corpus is a DAG, so this always
	// terminates).
	var tomlFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		tomlFiles = append(tomlFiles, filepath.Join(seedSkillsDir, e.Name()))
	}
	if len(tomlFiles) != 8 {
		t.Fatalf("seed skills dir %q has %d TOML files, want 8 (the R6 wizard corpus)", seedSkillsDir, len(tomlFiles))
	}

	imported := make(map[string]bool, len(tomlFiles))
	lastErr := make(map[string]error, len(tomlFiles))
	for pass := 0; pass < len(tomlFiles) && len(imported) < len(tomlFiles); pass++ {
		for _, path := range tomlFiles {
			if imported[path] {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read seed TOML %q: %v", path, err)
			}
			if _, err := store.ImportFromTOML(ctx, data); err != nil {
				lastErr[path] = err
				continue // dependency not yet imported OR genuinely missing; retry next pass
			}
			imported[path] = true
			delete(lastErr, path)
		}
	}

	wantImportable := map[string]bool{
		"linux.toml":   true,
		"java.toml":    true,
		"kotlin.toml":  true,
		"android.toml": true, // name: android.overview
	}
	for path := range wantImportable {
		full := filepath.Join(seedSkillsDir, path)
		if !imported[full] {
			t.Errorf("%s: expected to import successfully (its full requires-transitive-closure resolves within the 8-file corpus), but it did not; last error: %v", path, lastErr[full])
		}
	}
	if len(imported) != len(wantImportable) {
		t.Errorf("imported %d seed TOML files, want exactly %d (%v)", len(imported), len(wantImportable), wantImportable)
	}

	// The other 4 (cpp.toml, make.toml, cmake.toml, android_aosp.toml) MUST
	// remain un-imported, each failing with ErrDependencyNotFound (never
	// some OTHER, unrelated error -- that would mask a real regression
	// rather than confirm the expected hard-requires enforcement).
	wantBlocked := []string{"cpp.toml", "make.toml", "cmake.toml", "android_aosp.toml"}
	for _, path := range wantBlocked {
		full := filepath.Join(seedSkillsDir, path)
		if imported[full] {
			t.Errorf("%s: expected to remain blocked on a missing prerequisite skill, but it imported successfully", path)
			continue
		}
		err, ok := lastErr[full]
		if !ok {
			t.Errorf("%s: expected a captured ImportFromTOML error (never attempted?)", path)
			continue
		}
		if !errors.Is(err, ErrDependencyNotFound) {
			t.Errorf("%s: last ImportFromTOML error = %v, want it to wrap ErrDependencyNotFound (a genuinely missing prerequisite, e.g. c.language/python.language -- not some other failure)", path, err)
		}
	}

	skills, err := store.ListSkills(ctx, "", 100, 0)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != len(wantImportable) {
		t.Fatalf("ListSkills returned %d skills, want %d", len(skills), len(wantImportable))
	}
	for _, sk := range skills {
		if sk.Kind != models.SkillKindAtomic {
			t.Errorf("seed skill %q imported with Kind = %q, want %q (column DEFAULT, no seed TOML declares kind)", sk.Name, sk.Kind, models.SkillKindAtomic)
		}
	}

	// B1 fix (Fable code-review remediation): edge-count assertion. Under
	// the pre-fix dotted-tag TOMLSkillWrapper (`toml:"skill.dependencies"`),
	// BurntSushi/toml never matched the nested `[skill.dependencies]` table
	// against that tag (a dotted tag only matches a literal quoted key), so
	// wrapper.Dependencies decoded as an always-empty TOMLDependencies --
	// EVERY seed skill silently imported with ZERO dependency edges
	// regardless of its declared requires/extends/recommends, and
	// ImportFromTOML never errored (an empty list just resolves to zero
	// edges to create). android.overview (seed/skills/android.toml)
	// declares `requires = ["java.language", "kotlin.language"]` under
	// `[skill.dependencies]` -- post-fix this MUST resolve to exactly those
	// 2 requires edges. This also closes the pre-existing dead-import
	// defect noted on models.TOMLSkillWrapper: seed TOML import previously
	// never wired any dependency data end-to-end despite appearing to
	// succeed.
	androidOverview, err := store.GetByName(ctx, "android.overview")
	if err != nil {
		t.Fatalf("GetByName(android.overview): %v", err)
	}
	if len(androidOverview.Dependencies) != 2 {
		t.Fatalf("android.overview imported with %d dependency edges, want 2 (requires: java.language, kotlin.language) -- B1 dotted-tag TOML decode bug: got %+v",
			len(androidOverview.Dependencies), androidOverview.Dependencies)
	}
	gotRelTypeByName := make(map[string]models.DependencyType, len(androidOverview.Dependencies))
	for _, d := range androidOverview.Dependencies {
		gotRelTypeByName[d.DependsOnName] = d.RelationType
	}
	for _, wantName := range []string{"java.language", "kotlin.language"} {
		relType, ok := gotRelTypeByName[wantName]
		if !ok {
			t.Errorf("android.overview missing expected requires edge to %q; got edges to %v", wantName, gotRelTypeByName)
			continue
		}
		if relType != models.DepTypeRequires {
			t.Errorf("android.overview -> %s edge relation_type = %q, want %q", wantName, relType, models.DepTypeRequires)
		}
	}

	// B1 fix, composes/related_to coverage: none of the 8 seed TOMLs
	// declare composes/related_to/alternative_to (grep-confirmed against
	// seed/skills/*.toml), so the edge-count assertion above -- while a
	// real regression guard for the actual bug -- only exercises the
	// requires relation type. This ad-hoc fixture (NOT part of the 8-file
	// R6 seed corpus, and NOT persisted -- resolving composes/related_to
	// into stored skill_dependencies rows is still G07 scope, see
	// models.TOMLDependencies's doc comment) decodes a skill definition
	// declaring both composes and related_to under `[skill.dependencies]`
	// directly and asserts the parsed slices are non-empty and match the
	// declared names, proving the B1 nested-table decode fix is not
	// requires-specific.
	composesFixtureTOML := `
[skill]
name = "p1t1.m10.composes_related_to_fixture"
version = "0.1.0"
title = "M10 composes/related_to TOML-decode fixture"
description = "Ad-hoc fixture proving [skill.dependencies].composes/.related_to decode as non-empty slices post-B1-fix; not persisted since composes/related_to DB resolution is G07 scope."
content = "fixture content"
kind = "composite"

[skill.dependencies]
requires = []
extends = []
recommends = []
composes = ["java.language", "kotlin.language"]
related_to = ["android.overview"]
`
	var fixtureWrapper models.TOMLSkillWrapper
	if err := toml.Unmarshal([]byte(composesFixtureTOML), &fixtureWrapper); err != nil {
		t.Fatalf("toml.Unmarshal composes/related_to fixture: %v", err)
	}
	if got, want := fixtureWrapper.Skill.Dependencies.Composes, []string{"java.language", "kotlin.language"}; !equalStringSlices(got, want) {
		t.Errorf("fixture Skill.Dependencies.Composes = %v, want %v (B1 dotted-tag TOML decode bug)", got, want)
	}
	if got, want := fixtureWrapper.Skill.Dependencies.RelatedTo, []string{"android.overview"}; !equalStringSlices(got, want) {
		t.Errorf("fixture Skill.Dependencies.RelatedTo = %v, want %v (B1 dotted-tag TOML decode bug)", got, want)
	}

	// validate_dag.py must still report the corpus a valid DAG.
	validatorPath := filepath.Join(realSeedDirFromSkillPkg, "validate_dag.py")
	corpusPath := filepath.Join(realSeedDirFromSkillPkg, "CORPUS.yaml")
	cmd := exec.Command("python3", validatorPath, corpusPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("validate_dag.py %s: expected exit 0, got error %v; output:\n%s", corpusPath, err, out)
	}
}
