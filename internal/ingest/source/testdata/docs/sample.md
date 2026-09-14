# Sample Skill Document

This is a small, fully offline fixture used by
`internal/ingest/source` and `internal/ingest/pipeline` unit and
integration tests. It intentionally already starts with a Markdown
H1 heading so normalization tests can assert that an existing
heading is preserved rather than a synthetic one being prepended.

It describes a trivial skill: how to greet a user in Go.

```go
fmt.Println("hello, skills")
```
