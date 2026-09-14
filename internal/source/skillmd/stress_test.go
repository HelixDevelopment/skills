package skillmd

import (
	"sync"
	"testing"
)

// TestStress_ConcurrentParse exercises concurrent Parse calls on valid
// skill markdown. N=100 goroutines, no races expected.
func TestStress_ConcurrentParse(t *testing.T) {
	raw := []byte(`---
name: test-skill
version: "1.0.0"
title: Test Skill
description: A test skill for stress testing
kind: atomic
status: active
dependencies:
  - name: dep-one
    version: ">=0.1.0"
---

# Test Skill

This is the content of the test skill.

## Usage

Use it like this.
`)

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parsed, err := Parse(raw, "test.md")
			if err != nil {
				errs <- err
				return
			}
			if parsed.Name != "test-skill" {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Parse failed: %v", err)
		}
	}
}

// TestStress_ConcurrentParseMinimal exercises concurrent Parse calls on
// minimal markdown (no frontmatter). N=100 goroutines.
func TestStress_ConcurrentParseMinimal(t *testing.T) {
	raw := []byte(`# Minimal Skill

Just a title and some content.
`)

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Parse(raw, "minimal.md")
		}()
	}
	wg.Wait()
}
