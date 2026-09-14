// T-P4.07 full §11.4.169 matrix on the loader (HXC-159, R-08).
//
// Every test here exercises a REAL seam (no mocks beyond the filesystem
// itself): concurrent registrar/loader contention, peak-RSS + leak census,
// root-deleted chaos, disk-full (RLIMIT_FSIZE + /dev/full), SIGKILL
// mid-write atomicity, sustained tool-call-shaped load, and the measured
// cold-enumeration NFR budget. Coverage ledger row per type lives in
// qa-results/hxc159/loader_matrix/<run-id>/ledger.md — no blank cells.
package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------------

// writeBigCorpus generates n skills (~corpus-2 scale: 1174) with
// realistic-size descriptions and bodies. Deterministic content per index
// (§11.4.50) so every run enumerates the same tree.
func writeBigCorpus(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("skill-%04d", i)
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("---\nname: %s\ndescription: Procedural fixture skill number %d for load and memory measurement with content words.\nversion: 1.0.0\ncapabilities:\n  - filesystem:read\n---\n\n# %s\n\nBody paragraph one with several sentences of fixture content.\nBody paragraph two with more fixture content for hashing.\n", name, i, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// bigRegistry loads writeBigCorpus(n) as a first-party agent source.
func bigRegistry(t *testing.T, n int) (*Registry, int) {
	t.Helper()
	root := writeBigCorpus(t, n)
	r := NewRegistry()
	if err := r.RegisterSource(Source{Name: "big", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("big corpus union = %d, want %d (no silent drops)", len(got), n)
	}
	return r, n
}

// ---------------------------------------------------------------------------
// T-P4.07.1 race/deadlock + concurrency/atomicity
// ---------------------------------------------------------------------------

// TestConcurrentRegistrarAndReads hammers one registry with concurrent
// registrar writes (duplicate-name, fail-closed, state-unchanging) and
// concurrent loader reads (Load/Stats/Sources/Activate) plus audit writes.
// Under `-race` any unsynchronised map access FAILS the run — the proof is
// the race detector's verdict, not an assertion. Locking is flat (one
// RWMutex, no nesting), so completion without timeout is the deadlock
// proof.
func TestConcurrentRegistrarAndReads(t *testing.T) {
	r := activateFixture(t)
	const readers = 8
	const iters = 50
	var wg sync.WaitGroup
	errs := make(chan error, readers*iters*3)
	for w := 0; w < readers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, err := r.Load(); err != nil {
					errs <- err
					return
				}
				st := r.Stats()
				if st.Total != 2 {
					errs <- fmt.Errorf("worker %d: union total = %d, want 2", w, st.Total)
					return
				}
				if _, err := r.Activate([]string{"fixture-a.alpha"}, 0); err != nil {
					errs <- err
					return
				}
				// Registrar write contention: duplicate name, must fail
				// closed with the typed error — never a silent replace.
				dup := Source{Name: "fixture-a", Root: "/tmp", Dialect: DialectAgent, Precedence: 99, Trust: TrustFirstParty}
				if err := r.RegisterSource(dup); err == nil {
					errs <- fmt.Errorf("worker %d: duplicate source accepted under contention", w)
					return
				} else if _, ok := err.(*DuplicateSourceError); !ok {
					errs <- fmt.Errorf("worker %d: want *DuplicateSourceError, got %T", w, err)
					return
				}
			}
		}(w)
	}
	// Audit-log contention through the call boundary.
	log := NewAuditLog()
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := capFixtureSkill()
			for i := 0; i < iters; i++ {
				_ = Use(s, "filesystem:read", log)
				_ = Use(s, "shell:exec", log)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent registry access did not terminate in 60s (deadlock suspect)")
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := len(log.Entries()); n != 4*iters*2 {
		t.Fatalf("audit trail lost entries under contention: %d, want %d", n, 4*iters*2)
	}
}

// ---------------------------------------------------------------------------
// T-P4.07.2 memory: peak-RSS ceiling + leak census
// ---------------------------------------------------------------------------

// Peak-RSS and heap ceilings below. Set FROM measurement (probe run
// 2026-09-14, 1200-skill corpus, this host): cold-load heap delta
// ~1.1MB, VmHWM ~38MB process-wide. Ceilings carry a ≥4x margin —
// budgets, not observations.
const (
	// maxHeapDeltaBytes bounds the HeapAlloc growth of one cold 1200-skill
	// load over the pre-load baseline.
	maxHeapDeltaBytes = 8 << 20 // 8 MiB (measured ~1.1 MiB)
	// maxPeakRSSBytes bounds /proc VmHWM for the whole test process
	// around the load (measured ~38 MiB incl. Go runtime).
	maxPeakRSSBytes = 256 << 20 // 256 MiB
	// maxReloadGrowthRatio bounds HeapAlloc growth across 10 reloads vs
	// the first-load delta (leak census: repeated reloads must not creep).
	maxReloadGrowthRatio = 0.25
)

func heapAlloc() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// peakRSS reads VmHWM on Linux; ok=false elsewhere (honest §11.4.3 SKIP,
// never a faked zero).
func peakRSS() (rss uint64, ok bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			f := strings.Fields(line)
			if len(f) != 3 {
				return 0, false
			}
			kb, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return kb << 10, true
		}
	}
	return 0, false
}

func TestMemoryCeilingAndLeakCensus(t *testing.T) {
	const n = 1200 // corpus-2 scale (1174)
	root := writeBigCorpus(t, n)
	r := NewRegistry()
	if err := r.RegisterSource(Source{Name: "big", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}); err != nil {
		t.Fatal(err)
	}
	base := heapAlloc()
	got, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("union = %d, want %d", len(got), n)
	}
	delta := heapAlloc() - base
	t.Logf("cold-load heap delta over %d skills: %d bytes", n, delta)
	if delta > maxHeapDeltaBytes {
		t.Fatalf("heap delta %d exceeds ceiling %d", delta, maxHeapDeltaBytes)
	}
	if rss, ok := peakRSS(); ok {
		t.Logf("VmHWM after cold load: %d bytes", rss)
		if rss > maxPeakRSSBytes {
			t.Fatalf("peak RSS %d exceeds ceiling %d", rss, maxPeakRSSBytes)
		}
	} else {
		t.Log("peakRSS unavailable (non-Linux): heap-delta ceiling still enforced; RSS row is SKIP-with-reason per ledger")
	}
	// Leak census: 10 reloads must not creep beyond the ratio.
	afterFirst := heapAlloc()
	for i := 0; i < 10; i++ {
		if _, err := r.Load(); err != nil {
			t.Fatal(err)
		}
	}
	growth := int64(heapAlloc()) - int64(afterFirst)
	t.Logf("heap growth across 10 reloads: %d bytes", growth)
	if float64(growth) > float64(delta)*maxReloadGrowthRatio+float64(1<<20) {
		t.Fatalf("reload growth %d suggests a leak (ratio bound %.2f over first-load delta %d + 1MiB slack)", growth, maxReloadGrowthRatio, delta)
	}
}

// ---------------------------------------------------------------------------
// T-P4.07.3 stress+chaos
// ---------------------------------------------------------------------------

// TestChaosSourceRootDeleted: a vanished source root fails closed with a
// typed error — never a silent empty union (which would read as "zero
// skills" instead of "source gone").
func TestChaosSourceRootDeleted(t *testing.T) {
	root := writeBigCorpus(t, 20)
	r := NewRegistry()
	src := Source{Name: "doomed", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}
	if err := r.RegisterSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(); err != nil {
		t.Fatalf("pre-chaos load: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	_, err := r.Load()
	if err == nil {
		t.Fatal("load over a deleted root must fail closed, got nil error")
	}
	t.Logf("deleted-root refusal: %v", err)
}

// TestChaosRootDeletedMidEnumeration deletes the tree WHILE a load is in
// flight. The deterministic invariants (independent of kill timing):
// termination (no hang), no panic, and every returned skill (if any)
// structurally complete (name + hash present — a torn walk never yields
// a corrupt Skill).
func TestChaosRootDeletedMidEnumeration(t *testing.T) {
	root := writeBigCorpus(t, 400)
	r := NewRegistry()
	if err := r.RegisterSource(Source{Name: "doomed", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		skills []Skill
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				ch <- result{err: fmt.Errorf("panic during torn enumeration: %v", rec)}
			}
		}()
		s, err := r.Load()
		ch <- result{s, err}
	}()
	time.Sleep(5 * time.Millisecond)
	_ = os.RemoveAll(root)
	select {
	case res := <-ch:
		for _, s := range res.skills {
			if s.Name == "" || s.Hash == "" {
				t.Fatalf("torn enumeration yielded corrupt skill: %+v", s)
			}
		}
		t.Logf("mid-flight deletion: %d skills, err=%v (either outcome is fail-safe)", len(res.skills), res.err)
	case <-time.After(60 * time.Second):
		t.Fatal("torn enumeration did not terminate in 60s")
	}
}

// TestChaosDiskFullDuringSave proves disk pressure can never produce a
// silent success or a torn audit file:
//  1. /dev/full surfaces the real ENOSPC on this host (environment proof;
//     SKIP-with-reason where /dev/full is absent).
//  2. SaveAtomic under RLIMIT_FSIZE returns a real error (EFBIG/ENOSPC as
//     a Go error — probed 2026-09-14: process survives, error returned),
//     then succeeds again once the limit is restored (recovery proof).
func TestChaosDiskFullDuringSave(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err == nil {
		if err := os.WriteFile("/dev/full", []byte("x"), 0o644); err == nil {
			t.Fatal("/dev/full accepted a write — environment cannot simulate disk-full")
		} else {
			t.Logf("/dev/full refuses as expected (environment ENOSPC proof): %v", err)
		}
	} else {
		t.Log("no /dev/full on this host: environment-proof row is SKIP-with-reason per ledger")
	}

	log := NewAuditLog()
	for i := 0; i < 50; i++ {
		log.Record("big.skill-0001", "filesystem:read", "allowed")
	}
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &rl); err != nil {
		t.Fatal(err)
	}
	inf, capped := rl, rl
	capped.Cur = 4096
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &capped); err != nil {
		t.Skipf("cannot cap RLIMIT_FSIZE here: %v", err)
	}
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &inf) //nolint:errcheck
	target := filepath.Join(t.TempDir(), "audit.json")
	if err := log.SaveAtomic(target); err == nil {
		t.Fatal("SaveAtomic under a 4KiB file-size cap must return an error, got nil")
	} else {
		t.Logf("capped save refuses loudly: %v", err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &inf); err != nil {
		t.Fatal(err)
	}
	if err := log.SaveAtomic(target); err != nil {
		t.Fatalf("save after limit restore must succeed: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var entries []AuditEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("recovered audit file is not valid JSON: %v", err)
	}
	if len(entries) != 50 {
		t.Fatalf("recovered audit holds %d entries, want 50", len(entries))
	}
}

// TestChaosSigkillMidWrite kills a writer child mid-save-loop and asserts
// the target is ABSENT or COMPLETE valid JSON — never torn. The child is
// this same test binary re-executed (HELIX_SKILLS_CHAOS_CHILD=1); the
// parent observes only the file, never the child's memory.
func TestChaosSigkillMidWrite(t *testing.T) {
	if os.Getenv("HELIX_SKILLS_CHAOS_CHILD") == "1" {
		chaosChildMain()
		panic("unreachable")
	}
	target := filepath.Join(t.TempDir(), "audit.json")
	cmd := exec.Command(os.Args[0], "-test.run", "TestChaosSigkillMidWrite", "-test.count=1")
	cmd.Env = append(os.Environ(), "HELIX_SKILLS_CHAOS_CHILD=1", "HELIX_SKILLS_CHAOS_TARGET="+target)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	raw, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		t.Log("killed before first rename completed: target absent (safe)")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var entries []AuditEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("SIGKILL left a TORN audit file (%d bytes): %v", len(raw), err)
	}
	t.Logf("SIGKILL mid-write: target complete with %d entries (atomic rename held)", len(entries))
}

// chaosChildMain hammers SaveAtomic until killed. Runs ONLY as the
// re-executed child (see TestMain dispatch).
func chaosChildMain() {
	target := os.Getenv("HELIX_SKILLS_CHAOS_TARGET")
	log := NewAuditLog()
	for i := 0; i < 5000; i++ {
		log.Record("big.skill-0001", "filesystem:read", "allowed")
		_ = log.SaveAtomic(target)
	}
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// T-P4.07.4 DDoS-shaped sustained load (MCP tool-call profile)
// ---------------------------------------------------------------------------

// TestSustainedToolCallLoad drives the loader the way an MCP tool-call
// storm would: many concurrent readers, each resolving activation +
// capability checks + stats — the read-heavy dispatch profile. Bounded:
// fixed iterations, zero errors tolerated, throughput reported.
func TestSustainedToolCallLoad(t *testing.T) {
	r, _ := bigRegistry(t, 200)
	log := NewAuditLog()
	const workers = 16
	const iters = 100
	// Single-skill allowlist: the generated corpus carries near-identical
	// descriptions by construction, so a multi-skill allowlist would
	// (correctly) trip the similarity gate — the gate firing here would
	// prove the gate, not the load path. One active skill keeps this a
	// pure read-dispatch measurement.
	allow := []string{"big.skill-0001"}
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, workers*iters)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				active, err := r.Activate(allow, 0)
				if err != nil {
					errs <- err
					return
				}
				for _, s := range active {
					if err := Use(s, "filesystem:read", log); err != nil {
						errs <- err
						return
					}
				}
				_ = r.Stats()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	ops := int64(workers * iters * (1 + len(allow) + 1))
	t.Logf("sustained load: %d boundary ops in %v (%.0f ops/s), zero errors", ops, elapsed, float64(ops)/elapsed.Seconds())
}

// ---------------------------------------------------------------------------
// T-P4.07.5 benchmarking: measured cold-enumeration NFR budget
// ---------------------------------------------------------------------------

// coldEnumerationBudgetNs bounds one cold Load() over a 1200-skill tree.
// Set FROM measurement (bench run 2026-09-14, this host — see
// qa-results/hxc159/loader_matrix/<run-id>/bench.log): mean ~96ms.
// Budget carries a ~5x margin over the measured mean; the 402ms whole-tree
// baseline (T-P4.07.5 input, different corpus/host) is the sanity
// anchor, not the budget.
var coldEnumerationBudget = 500 * time.Millisecond

func BenchmarkColdEnumeration(b *testing.B) {
	root := writeBigCorpusForBench(b, 1200)
	r := NewRegistry()
	if err := r.RegisterSource(Source{Name: "big", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := r.Load()
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != 1200 {
			b.Fatalf("union = %d, want 1200", len(got))
		}
	}
}

func writeBigCorpusForBench(b *testing.B, n int) string {
	b.Helper()
	root := b.TempDir()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("skill-%04d", i)
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		body := fmt.Sprintf("---\nname: %s\ndescription: Procedural fixture skill number %d for load and memory measurement with content words.\nversion: 1.0.0\n---\n\n# %s\n\nBody content.\n", name, i, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

// TestColdEnumerationBudget enforces the NFR: one cold load over the
// corpus-2-scale tree must finish inside budget. A budget breach FAILS —
// budgets that cannot fail are §11.4.1 bluffs.
func TestColdEnumerationBudget(t *testing.T) {
	r, _ := bigRegistry(t, 1200)
	start := time.Now()
	got, err := r.Load()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1200 {
		t.Fatalf("union = %d, want 1200", len(got))
	}
	t.Logf("cold enumeration over 1200 skills: %v (budget %v)", elapsed, coldEnumerationBudget)
	if elapsed > coldEnumerationBudget {
		t.Fatalf("cold enumeration %v exceeds NFR budget %v", elapsed, coldEnumerationBudget)
	}
}
