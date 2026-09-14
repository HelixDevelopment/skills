package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakeSource: a trivial in-memory Source implementation. It exists ONLY to
// prove the Source contract is implementable end-to-end (F1.1 acceptance
// criterion (2) in TRACKED_ITEMS.md) and to give other source-class unit
// tests a reusable, generic contract-test helper (assertSourceContract
// below) -- it is a unit-test-only fake, permitted under §11.4.27(A), and
// is NEVER imported by production code.
// ---------------------------------------------------------------------------

type fakeItem struct {
	path        string
	contentType string
	body        []byte
}

type fakeSource struct {
	id    string
	items []fakeItem
}

func newFakeSource(id string, items []fakeItem) *fakeSource {
	return &fakeSource{id: id, items: items}
}

func (f *fakeSource) ID() string { return f.id }

func (f *fakeSource) List(ctx context.Context) ([]ItemRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	refs := make([]ItemRef, 0, len(f.items))
	for _, it := range f.items {
		refs = append(refs, ItemRef{
			SourceID: f.id,
			Path:     it.path,
			Size:     int64(len(it.body)),
			ModTime:  time.Unix(0, 0).UTC(),
		})
	}
	return refs, nil
}

func (f *fakeSource) Fetch(ctx context.Context, ref ItemRef) (*RawItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.SourceID != f.id {
		return nil, errors.New("fakeSource: ref belongs to a different source")
	}
	for _, it := range f.items {
		if it.path == ref.Path {
			sum := sha256.Sum256(it.body)
			return &RawItem{
				Ref:         ref,
				ContentType: it.contentType,
				Body:        it.body,
				FetchedAt:   time.Now().UTC(),
				FetchedHash: hex.EncodeToString(sum[:]),
			}, nil
		}
	}
	return nil, errors.New("fakeSource: item not found")
}

func (f *fakeSource) Watchable() bool { return false }

func (f *fakeSource) Watch(context.Context, chan<- ItemRef) error {
	return errors.New("fakeSource: watch not supported")
}

// assertSourceContract exercises the generic, source-class-independent
// part of the Source contract: List enumerates every item, every listed
// item is independently Fetch-able, the fetched hash is a real sha256 of
// the fetched body, and Fetch rejects a ref whose SourceID belongs to a
// different source. Concrete source implementations' own tests (e.g.
// filesystem_test.go) call this helper against themselves so the contract
// is checked identically everywhere it applies.
func assertSourceContract(t *testing.T, src Source, wantPaths []string) {
	t.Helper()
	ctx := context.Background()

	refs, err := src.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	gotPaths := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.SourceID != src.ID() {
			t.Errorf("ItemRef.SourceID = %q, want %q", r.SourceID, src.ID())
		}
		gotPaths = append(gotPaths, r.Path)
	}
	sort.Strings(gotPaths)
	wantSorted := append([]string(nil), wantPaths...)
	sort.Strings(wantSorted)
	if len(gotPaths) != len(wantSorted) {
		t.Fatalf("List returned %d items %v, want %d items %v", len(gotPaths), gotPaths, len(wantSorted), wantSorted)
	}
	for i := range gotPaths {
		if gotPaths[i] != wantSorted[i] {
			t.Fatalf("List paths = %v, want %v", gotPaths, wantSorted)
		}
	}

	for _, ref := range refs {
		raw, err := src.Fetch(ctx, ref)
		if err != nil {
			t.Fatalf("Fetch(%q): %v", ref.Path, err)
		}
		sum := sha256.Sum256(raw.Body)
		wantHash := hex.EncodeToString(sum[:])
		if raw.FetchedHash != wantHash {
			t.Errorf("Fetch(%q).FetchedHash = %q, want real sha256 %q", ref.Path, raw.FetchedHash, wantHash)
		}
		if raw.Ref.Path != ref.Path {
			t.Errorf("Fetch(%q).Ref.Path = %q, want %q", ref.Path, raw.Ref.Path, ref.Path)
		}
	}

	// Cross-source ref rejection: a ref stamped with a foreign SourceID
	// must never be silently served.
	foreign := ItemRef{SourceID: "not-" + src.ID(), Path: "whatever"}
	if _, err := src.Fetch(ctx, foreign); err == nil {
		t.Error("Fetch with a foreign SourceID ref = nil error, want rejection")
	}
}

func TestFakeSource_ImplementsContract(t *testing.T) {
	fs := newFakeSource("fake:1", []fakeItem{
		{path: "a.md", contentType: "text/markdown", body: []byte("# A\n")},
		{path: "b.txt", contentType: "text/plain", body: []byte("b body")},
	})

	var _ Source = fs // compile-time contract check

	assertSourceContract(t, fs, []string{"a.md", "b.txt"})
}

func TestFakeSource_Watchable_False(t *testing.T) {
	fs := newFakeSource("fake:2", nil)
	if fs.Watchable() {
		t.Fatal("Watchable() = true, want false for fakeSource")
	}
	if err := fs.Watch(context.Background(), make(chan ItemRef)); err == nil {
		t.Fatal("Watch() = nil error, want an error when Watchable()==false")
	}
}
