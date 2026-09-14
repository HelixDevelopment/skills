package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

// fakeSourceForPipelineTest is a trivial in-memory, single-item Source
// implementation used ONLY by this package's own unit tests
// (§11.4.27(A) permits fakes in unit tests) to prove the pipeline never
// special-cases a concrete Source implementation -- it exercises the
// SAME source.Source interface FilesystemSource implements, with a
// different SourceID scheme, and never leaks into production code.
type fakeSourceForPipelineTest struct {
	id          string
	path        string
	contentType string
	body        []byte
}

func newFakeSourceForPipelineTest(id, path, contentType string, body []byte) *fakeSourceForPipelineTest {
	return &fakeSourceForPipelineTest{id: id, path: path, contentType: contentType, body: body}
}

func (f *fakeSourceForPipelineTest) ID() string { return f.id }

func (f *fakeSourceForPipelineTest) List(ctx context.Context) ([]source.ItemRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []source.ItemRef{{SourceID: f.id, Path: f.path, Size: int64(len(f.body))}}, nil
}

func (f *fakeSourceForPipelineTest) Fetch(ctx context.Context, ref source.ItemRef) (*source.RawItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Path != f.path {
		return nil, errors.New("fakeSourceForPipelineTest: unknown path")
	}
	sum := sha256.Sum256(f.body)
	return &source.RawItem{
		Ref:         ref,
		ContentType: f.contentType,
		Body:        f.body,
		FetchedAt:   time.Now().UTC(),
		FetchedHash: hex.EncodeToString(sum[:]),
	}, nil
}

func (f *fakeSourceForPipelineTest) Watchable() bool { return false }

func (f *fakeSourceForPipelineTest) Watch(context.Context, chan<- source.ItemRef) error {
	return errors.New("fakeSourceForPipelineTest: watch not supported")
}
