package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/codeanalysis"
)

// DefaultMaxItemSizeBytes mirrors the DESIGN.md §5 example
// `max_item_size_kb = 20000` default (20 MB) until the shared
// config.IngestionConfig section is wired by a separate work item
// (T0.1 in TRACKED_ITEMS.md, which edits internal/config/config.go and is
// out of scope for this additive-only package).
const DefaultMaxItemSizeBytes int64 = 20000 * 1024

// ErrOverMaxItemSize is returned by Fetch when an item's size exceeds the
// configured cap. Fetch never truncates -- it rejects cleanly.
var ErrOverMaxItemSize = errors.New("ingest/source: item exceeds max item size")

// ErrPathNotAllowed is returned by NewFilesystemSource when root does not
// canonicalize inside any of the supplied allowedRoots.
var ErrPathNotAllowed = errors.New("ingest/source: root is not inside any allowed root")

// ErrFetchPathRejected is returned by Fetch when ref.Path fails the
// re-validated canonical-path boundary check against s.root -- e.g. a
// ".." segment that resolves outside the source's root, or a symlink
// that would otherwise smuggle the escape past a purely lexical check.
// Callers can use errors.Is(err, ErrFetchPathRejected) to distinguish
// this specific rejection from any other Fetch failure (stat error, size
// cap, ...); the underlying codeanalysis rejection reason is still
// wrapped alongside it.
var ErrFetchPathRejected = errors.New("ingest/source: fetch path rejected")

// FilesystemSource implements Source over a local directory tree. It is
// the foundational, lowest-dependency concrete Source (F1.2 in
// TRACKED_ITEMS.md): a bounded, fail-closed, allowlisted directory walk +
// read.
//
// Watchable() returns false in this increment. DESIGN.md §1.1's source
// table marks the filesystem source's real-time recursive watch as a
// SEPARATE, later item (F3.1, internal/ingest/watch/directory.go,
// fsnotify-based); layering that watcher on top of THIS Source is
// explicitly excluded from this work item's scope. Reporting
// Watchable()==true here ahead of that watcher landing would be a bluff
// (§11.4.6 -- this is a deliberate, documented discrepancy vs.
// DESIGN.md §1.1's table, which is written from the perspective of the
// fully-landed feature; this file honestly reflects what THIS increment
// alone delivers).
type FilesystemSource struct {
	root             string
	allowedRoots     []string
	maxItemSizeBytes int64
	excludePatterns  []string
}

// Option configures a FilesystemSource.
type Option func(*FilesystemSource)

// WithMaxItemSizeBytes overrides DefaultMaxItemSizeBytes.
func WithMaxItemSizeBytes(n int64) Option {
	return func(s *FilesystemSource) { s.maxItemSizeBytes = n }
}

// WithExcludePatterns sets directory-name exclusion patterns evaluated
// with the same filepath.Match-or-substring semantics as
// codeanalysis.Analyzer.discoverFiles (internal/codeanalysis/analyzer.go),
// mirrored here deliberately so the two independent filesystem-scanning
// features share one exclusion-matching shape even though they do not
// share config wiring yet (config wiring is T0.1, a separate work item
// that edits internal/config/config.go and is out of scope here).
func WithExcludePatterns(patterns []string) Option {
	return func(s *FilesystemSource) { s.excludePatterns = patterns }
}

// NewFilesystemSource canonicalizes root and verifies it resolves inside
// AT LEAST ONE of allowedRoots via the EXISTING, imported (never
// duplicated) codeanalysis.ValidateProjectPath guard -- the same
// fail-closed traversal/symlink-escape check learn_from_project uses,
// applied per DESIGN.md §1.2's explicit reuse instruction. An empty
// allowedRoots fails closed (rejects every root, including root itself)
// because ValidateProjectPath itself fails closed on an empty
// allowedRoot.
func NewFilesystemSource(root string, allowedRoots []string, opts ...Option) (*FilesystemSource, error) {
	if len(allowedRoots) == 0 {
		// Mirror ValidateProjectPath's own fail-closed message shape for
		// an unset allowlist so the caller gets the same signal it would
		// get from calling ValidateProjectPath(root, "") directly.
		_, err := codeanalysis.ValidateProjectPath(root, "")
		return nil, err
	}

	var (
		canon   string
		lastErr error
		ok      bool
	)
	for _, allowed := range allowedRoots {
		c, err := codeanalysis.ValidateProjectPath(root, allowed)
		if err != nil {
			lastErr = err
			continue
		}
		canon, ok = c, true
		break
	}
	if !ok {
		return nil, fmt.Errorf("%w: %q against %v: %w", ErrPathNotAllowed, root, allowedRoots, lastErr)
	}

	fsrc := &FilesystemSource{
		root:             canon,
		allowedRoots:     append([]string(nil), allowedRoots...),
		maxItemSizeBytes: DefaultMaxItemSizeBytes,
	}
	for _, opt := range opts {
		opt(fsrc)
	}
	return fsrc, nil
}

// ID returns a stable identifier of the form "fs:<canonical root>".
func (s *FilesystemSource) ID() string {
	return "fs:" + s.root
}

// Watchable always returns false for this increment. See the type-level
// doc comment.
func (s *FilesystemSource) Watchable() bool { return false }

// Watch is not implemented in this increment (see Watchable). It returns
// a clear, typed error rather than blocking or panicking, so a caller bug
// (calling Watch without checking Watchable() first) still fails safely
// and observably.
func (s *FilesystemSource) Watch(_ context.Context, _ chan<- ItemRef) error {
	return errors.New("ingest/source: FilesystemSource.Watch not implemented in this increment (tracked follow-up F3.1); check Watchable() first")
}

// List performs a bounded, exclude-pattern-aware recursive walk of the
// source root, mirroring codeanalysis.Analyzer.discoverFiles's shape
// (internal/codeanalysis/analyzer.go): directories matching an exclude
// pattern are skipped entirely (filepath.SkipDir), unreadable entries are
// skipped rather than aborting the whole walk, and the walk honors ctx
// cancellation. Oversized files (over maxItemSizeBytes) are still listed
// -- List only enumerates; Fetch is what enforces the size cap, and it
// does so by REJECTING rather than truncating (see Fetch).
func (s *FilesystemSource) List(ctx context.Context) ([]ItemRef, error) {
	var refs []ItemRef

	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == s.root {
				// The root itself vanished or became unreadable between
				// NewFilesystemSource and this List call (filepath.WalkDir
				// calls fn(root, nil, err) directly when os.Lstat(root)
				// itself fails, before any recursion). That is NOT the
				// same condition as "an empty, readable root with zero
				// items in it" -- conflating the two would silently
				// report a broken source as merely empty (§11.4.201).
				// Propagate the error instead of returning ([], nil).
				return err
			}
			return nil // skip entries we can't read, mirroring discoverFiles
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if d.IsDir() {
			if path != s.root && s.excluded(d.Name(), path) {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil // skip unreadable entries
		}

		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		refs = append(refs, ItemRef{
			SourceID: s.ID(),
			Path:     rel,
			Size:     info.Size(),
			ModTime:  info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// excluded reports whether name or the full path matches one of the
// configured exclude patterns, using the same
// filepath.Match-OR-strings.Contains semantics as
// codeanalysis.Analyzer.discoverFiles.
func (s *FilesystemSource) excluded(name, path string) bool {
	for _, pattern := range s.excludePatterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

// Fetch reads the file at ref.Path (which MUST be a path previously
// returned by List, source-relative and slash-separated) and returns its
// bytes + a best-effort content-type classification + a sha256 hash.
//
// Fetch is FAIL-CLOSED on size: it stats the file first and rejects
// (ErrOverMaxItemSize) BEFORE reading if the file exceeds
// maxItemSizeBytes, and it re-checks the actually-read length afterward
// (defends a stat-then-grow race) -- it never truncates and silently
// returns a partial item.
//
// Fetch also fails closed against a ref.Path that escapes s.root (e.g. an
// absolute path, or one containing ".." that resolves outside root) --
// every fetched path is re-validated through the same
// codeanalysis.ValidateProjectPath guard used at construction time, not
// merely trusted because it looks source-relative.
func (s *FilesystemSource) Fetch(ctx context.Context, ref ItemRef) (*RawItem, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if ref.SourceID != s.ID() {
		return nil, fmt.Errorf("ingest/source: ItemRef.SourceID %q does not match this source %q", ref.SourceID, s.ID())
	}
	if strings.TrimSpace(ref.Path) == "" {
		return nil, errors.New("ingest/source: empty ItemRef.Path")
	}

	full := filepath.Join(s.root, filepath.FromSlash(ref.Path))
	canonFull, err := codeanalysis.ValidateProjectPath(full, s.root)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrFetchPathRejected, ref.Path, err)
	}

	stat, err := os.Stat(canonFull)
	if err != nil {
		return nil, fmt.Errorf("ingest/source: stat %q: %w", ref.Path, err)
	}
	if stat.IsDir() {
		return nil, fmt.Errorf("ingest/source: %q is a directory, not a fetchable item", ref.Path)
	}
	if stat.Size() > s.maxItemSizeBytes {
		return nil, fmt.Errorf("%w: %q is %d bytes, cap is %d bytes", ErrOverMaxItemSize, ref.Path, stat.Size(), s.maxItemSizeBytes)
	}

	body, err := os.ReadFile(canonFull)
	if err != nil {
		return nil, fmt.Errorf("ingest/source: read %q: %w", ref.Path, err)
	}
	if int64(len(body)) > s.maxItemSizeBytes {
		// Defends a stat-then-grow race: a file could grow between the
		// os.Stat check above and this os.ReadFile call (e.g. a
		// concurrent writer appending to it). This re-check catches that
		// case and rejects rather than silently returning a partial or
		// over-cap item.
		//
		// Honest boundary (§11.4.6/§11.4.194): this specific race has NO
		// dedicated kill test in this package. The os.Stat pre-check
		// above makes the race window a few microseconds wide on a real
		// filesystem, and reliably hitting it hermetically would need an
		// injectable filesystem/fault seam (e.g. an io/fs.FS abstraction
		// or a stat-then-read hook) that this increment does not have.
		// This is a documented, deliberate gap -- not a silently
		// assumed-safe path -- and a future increment adding such a seam
		// should add the kill test alongside it.
		return nil, fmt.Errorf("%w: %q grew to %d bytes while reading, cap is %d bytes", ErrOverMaxItemSize, ref.Path, len(body), s.maxItemSizeBytes)
	}

	sum := sha256.Sum256(body)
	return &RawItem{
		Ref:         ref,
		ContentType: contentTypeForExt(ref.Path),
		Body:        body,
		FetchedAt:   time.Now().UTC(),
		FetchedHash: hex.EncodeToString(sum[:]),
	}, nil
}

// contentTypeForExt classifies a file's content type from its extension.
// This increment only needs to distinguish text/markdown and text/plain
// (the only two classes internal/ingest/pipeline's EXTRACT stage can
// currently handle -- see internal/ingest/pipeline/extract.go); every
// other extension is classified honestly as application/octet-stream
// rather than guessed, and the EXTRACT stage rejects it with a typed
// unsupported-content-type error rather than silently mis-parsing it.
func contentTypeForExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
