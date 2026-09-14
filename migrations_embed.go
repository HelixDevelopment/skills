// Package skillsystem is the module-root package. Its sole purpose is to embed
// the SQL schema-migration files into the compiled binary so cmd/server applies
// them deterministically, independent of the process working directory
// (§G23 / research/ops_hardening_design.md).
//
// WHY THIS FILE LIVES AT THE MODULE ROOT (go:embed placement, §11.4.6).
// A //go:embed directive may only reference files at or below the directory of
// the Go source file that carries it, and the pattern may not contain ".." path
// elements. The migrations live at <module root>/migrations/*.sql. Neither
// internal/db nor cmd/server can reach them (`//go:embed ../../migrations/*.sql`
// is rejected by the compiler), so the embed MUST live in a package rooted at
// the module root — this file. The design's DECISION names the directive
// literally as `//go:embed migrations/*.sql`, which resolves only from here.
package skillsystem

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrationsFS is the embedded migrations directory, re-rooted at "migrations"
// so its entries are the bare NNN_description.{up,down}.sql filenames — exactly
// the shape os.DirFS(dir) yields for the on-disk directory. That equivalence is
// what lets db.MigrateFS treat an embedded FS and an on-disk directory
// identically (fs.ReadDir(".") + fs.ReadFile(<base name>)).
var MigrationsFS fs.FS = mustSub(migrationFiles, "migrations")

// mustSub returns fs.Sub(fsys, dir) or panics. A failure is unreachable in a
// correctly-built binary: the //go:embed above guarantees the "migrations"
// subtree exists at compile time. Panicking (rather than degrading to a nil /
// empty FS) refuses to ship a binary that silently cannot see its own
// migrations — the fail-closed posture §G23 demands.
func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("skillsystem: embed migrations sub-FS: " + err.Error())
	}
	return sub
}
