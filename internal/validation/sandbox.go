// Package validation implements the fail-closed, NON-EXECUTING skill validation
// pipeline for the HelixKnowledge system (the "zero-bluff guarantee").
//
// The default StaticValidator (see pipeline.go) executes NOTHING untrusted. It
// verifies resources (SSRF-guarded, fail-closed hashing), performs a
// non-executing static code check (memory-safe standard-library front-ends
// only — e.g. go/parser, in-process, no subprocess), cross-references the
// dependency graph, and requires a real multi-model LLM jury verdict. Because
// there is NO host-execution code path anywhere in this package, the class of
// remote-code-execution defect that a "sandbox" running untrusted snippets on
// the host (go run / python -c / bash -c) represented is closed BY CONSTRUCTION
// (gaps G02 / G16): there is no host-exec code left to misconfigure.
//
// Execution of untrusted code is an OPT-IN, OFF-BY-DEFAULT capability modelled
// by the IsolatedExecutor interface. Its only shipped implementation,
// SkipIsolatedExecutor, ALWAYS returns a SKIP verdict with reason
// "isolation_runtime_absent" — it never falls back to host execution and never
// auto-passes. A concrete rootless-Podman executor is submodule-gated (the
// containers submodule is absent) and intentionally not built here.
package validation

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
)

// ---------------------------------------------------------------------------
// Per-stage verdict vocabulary (fail-closed aggregation)
// ---------------------------------------------------------------------------

// StageStatus is the closed set of per-stage verdicts produced by the pipeline.
type StageStatus string

const (
	// StagePass is a real positive verdict for a stage.
	StagePass StageStatus = "PASS"
	// StageFail is a real negative verdict for a stage.
	StageFail StageStatus = "FAIL"
	// StageSkip means a stage could NOT produce a verdict (missing capability).
	// A SKIP is NEVER upgraded to an overall PASS.
	StageSkip StageStatus = "SKIP"
	// StageBlocked means a stage refused fail-closed (e.g. an empty jury while
	// validation is enabled). A BLOCKED is NEVER an overall PASS.
	StageBlocked StageStatus = "BLOCKED"
	// StageNA means a stage is genuinely not applicable (no such content, e.g. a
	// skill with no resources or no code). N/A neither proves nor blocks.
	StageNA StageStatus = "N/A"
)

// computeOverallVerdict implements the fail-closed aggregation rule (§G03/§G05):
// a skill PASSES only when every stage produced a real positive verdict (PASS)
// or was genuinely not applicable (N/A), AND at least one stage was a real PASS.
// A SKIP, FAIL, or BLOCKED in ANY stage never upgrades to an overall PASS.
func computeOverallVerdict(stages map[string]StageStatus) bool {
	if len(stages) == 0 {
		return false // nothing ran => nothing proven => fail-closed
	}
	sawPositive := false
	for _, st := range stages {
		switch st {
		case StagePass:
			sawPositive = true
		case StageNA:
			// not applicable — neither proves nor blocks
		default: // SKIP / FAIL / BLOCKED / unknown => fail-closed
			return false
		}
	}
	return sawPositive
}

// DecideCreateStatus computes the persisted status of a newly-submitted skill
// under the fail-closed create-path policy (§G03 request-path): a skill may be
// promoted beyond "draft" ONLY when validation is enabled AND produced a real
// positive verdict. A client can never self-promote to validated/active without
// a passing verdict.
func DecideCreateStatus(enabled bool, requested models.SkillStatus, res *ValidationResult) models.SkillStatus {
	if !enabled || res == nil || !res.Passed {
		return models.SkillStatusDraft
	}
	// Passed: honour an explicit request to activate; otherwise mark validated.
	if requested == models.SkillStatusActive {
		return models.SkillStatusActive
	}
	return models.SkillStatusValidated
}

// ---------------------------------------------------------------------------
// Tier B — IsolatedExecutor (opt-in, OFF by default, fail-closed)
// ---------------------------------------------------------------------------

// IsolatedResult captures the outcome of an opt-in isolated execution attempt.
type IsolatedResult struct {
	Status StageStatus `json:"status"` // PASS / FAIL / SKIP — never from host execution
	Reason string      `json:"reason"`
	Stdout string      `json:"stdout,omitempty"`
	Stderr string      `json:"stderr,omitempty"`
}

// IsolatedExecutor is the OPT-IN, OFF-BY-DEFAULT tier that may observe the
// output of a first-party POC run inside a real isolation boundary. It is NEVER
// used to run untrusted skill snippets on the host. Implementations MUST fail
// CLOSED: when the isolation runtime is unavailable they return StageSkip, never
// host execution and never an auto-pass.
type IsolatedExecutor interface {
	Execute(ctx context.Context, code, language string, timeout time.Duration) (*IsolatedResult, error)
}

// SkipIsolatedExecutor is the ONLY shipped IsolatedExecutor. It always SKIPs
// with reason "isolation_runtime_absent": the concrete rootless-Podman executor
// is submodule-gated and intentionally not built. This guarantees that with no
// isolation runtime present, the execution tier never falls back to host exec.
type SkipIsolatedExecutor struct{}

// NewSkipIsolatedExecutor returns the default fail-closed executor.
func NewSkipIsolatedExecutor() SkipIsolatedExecutor { return SkipIsolatedExecutor{} }

// Execute always returns a SKIP verdict; it executes nothing whatsoever.
func (SkipIsolatedExecutor) Execute(_ context.Context, _ string, _ string, _ time.Duration) (*IsolatedResult, error) {
	return &IsolatedResult{Status: StageSkip, Reason: "isolation_runtime_absent"}, nil
}

// ---------------------------------------------------------------------------
// Non-executing static code check (default tier)
// ---------------------------------------------------------------------------

// staticCodeReport is the informational, NON-EXECUTING result of parsing the
// fenced code blocks in a skill's content. It never runs any code: Go blocks are
// parsed in-process via go/parser (memory-safe, no subprocess), and blocks in
// languages without an in-process front-end are recorded as unchecked (an honest
// coverage gap), never executed and never a hard failure for an illustrative
// documentation fragment.
type staticCodeReport struct {
	Total     int
	Checked   int // blocks run through an in-process non-executing front-end
	Unchecked int // blocks with no in-process front-end (recorded, not executed)
	Notes     []string
}

// staticCheckCode performs a NON-EXECUTING static check of the given code
// blocks. It never executes untrusted code under any circumstances.
func staticCheckCode(snippets []codeSnippet) staticCodeReport {
	rep := staticCodeReport{Total: len(snippets)}
	for i, sn := range snippets {
		switch normalizeLanguage(sn.Language) {
		case "go":
			if !goParses(sn.Code) {
				rep.Notes = append(rep.Notes,
					fmt.Sprintf("block %d (go): not a parseable Go file/fragment", i+1))
			}
			rep.Checked++
		default:
			// No in-process, non-executing front-end for this language: record
			// the coverage gap honestly. NEVER shell out to a toolchain to run it.
			rep.Unchecked++
		}
	}
	return rep
}

// goParses reports whether src parses as a Go file OR as a Go fragment wrapped in
// a function body. It only PARSES (go/parser, in-process); it never compiles,
// links, or executes anything.
func goParses(src string) bool {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "s.go", src, parser.SkipObjectResolution); err == nil {
		return true
	}
	wrapped := "package p\nfunc _() {\n" + src + "\n}\n"
	fset = token.NewFileSet()
	_, err := parser.ParseFile(fset, "s.go", wrapped, parser.SkipObjectResolution)
	return err == nil
}

// normalizeLanguage converts various language name formats to canonical forms.
func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "golang":
		return "go"
	case "py", "python3":
		return "python"
	case "js", "nodejs":
		return "javascript"
	case "sh":
		return "bash"
	case "ts":
		return "typescript"
	case "c++", "cxx", "cpp":
		return "cpp"
	case "rs":
		return "rust"
	default:
		return lang
	}
}

// ---------------------------------------------------------------------------
// SSRF egress guard (fail-closed) — used by resource verification (§G21)
// ---------------------------------------------------------------------------

// additionalBlockedRanges lists CIDR ranges that are NOT covered by the
// net.IP helper methods used above (loopback/link-local/private/multicast/
// unspecified) but MUST still be refused as SSRF egress targets:
//
//   - 0.0.0.0/8        "this host on this network" (RFC 1122 §3.2.1.3).
//     net.IP.IsUnspecified() catches only the exact 0.0.0.0; a non-zero host in
//     0.x.x.x (e.g. 0.1.2.3) is never a legitimate egress target and must be
//     refused for completeness (§G36).
//   - 100.64.0.0/10    carrier-grade NAT (RFC 6598) — CONTAINS the Alibaba
//     Cloud metadata endpoint 100.100.100.200.
//   - 240.0.0.0/4      reserved / Class E.
//   - 192.0.0.0/24     IETF protocol assignments (RFC 6890).
//   - 198.18.0.0/15    benchmarking (RFC 2544).
//   - 255.255.255.255/32 limited broadcast.
//   - 64:ff9b::/96     NAT64 well-known prefix (RFC 6052).
//
// Each CIDR is parsed exactly once, at package initialization, using the same
// net.ParseCIDR + Contains pattern as the rest of this guard; the literals are
// constants so mustParseCIDR can only ever panic on a programmer typo, never
// at request time.
var additionalBlockedRanges = []struct {
	net    *net.IPNet
	reason string
}{
	{mustParseCIDR("0.0.0.0/8"), "this host on this network (RFC 1122)"},
	{mustParseCIDR("100.64.0.0/10"), "carrier-grade NAT (RFC 6598, incl. Alibaba Cloud metadata 100.100.100.200)"},
	{mustParseCIDR("240.0.0.0/4"), "reserved (Class E)"},
	{mustParseCIDR("192.0.0.0/24"), "IETF protocol assignments"},
	{mustParseCIDR("198.18.0.0/15"), "benchmarking (RFC 2544)"},
	{mustParseCIDR("255.255.255.255/32"), "limited broadcast"},
	{mustParseCIDR("64:ff9b::/96"), "NAT64 well-known prefix"},
}

// mustParseCIDR parses a constant CIDR literal, once, at package init. It
// panics only on a programmer typo in one of the literals above — never on
// any request-time input.
func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("isBlockedIP: invalid constant CIDR %q: %v", s, err))
	}
	return n
}

// isBlockedIP reports whether an IP must be refused as an egress target:
// loopback, link-local (which includes the 169.254.169.254 cloud-metadata
// endpoint), unique-local / private (RFC1918 / ULA), the unspecified address,
// multicast, or one of the additionalBlockedRanges above (CGNAT/metadata,
// reserved, IETF protocol assignments, benchmarking, limited broadcast,
// NAT64). This closes the SSRF vector where a skill-supplied URL points the
// server at internal or metadata services.
func isBlockedIP(ip net.IP) (bool, string) {
	switch {
	case ip == nil:
		return true, "unparseable ip"
	case ip.IsLoopback():
		return true, "loopback"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return true, "link-local (incl. cloud metadata 169.254.169.254)"
	case ip.IsUnspecified():
		return true, "unspecified"
	case ip.IsMulticast():
		return true, "multicast"
	case ip.IsPrivate():
		return true, "private (RFC1918/ULA)"
	}
	for _, r := range additionalBlockedRanges {
		if r.net.Contains(ip) {
			return true, r.reason
		}
	}
	return false, ""
}

// screenHost refuses any host that resolves to a blocked egress target. A host
// literal is checked directly; a name is resolved and EVERY resolved address
// must be allowed. A resolution error is fail-closed (returns an error): an
// unresolvable host cannot be proven safe.
func screenHost(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if blocked, reason := isBlockedIP(ip); blocked {
			return fmt.Errorf("egress to %s blocked: %s", host, reason)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve host %q (fail-closed): %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses (fail-closed)", host)
	}
	for _, ipa := range ips {
		if blocked, reason := isBlockedIP(ipa.IP); blocked {
			return fmt.Errorf("egress to %s (%s) blocked: %s", host, ipa.IP, reason)
		}
	}
	return nil
}

// newGuardedHTTPClient builds an http.Client whose dialer refuses, AT CONNECT
// TIME, any address that isBlockedIP flags — closing DNS-rebinding where a name
// passes the pre-flight screen but resolves to an internal address at dial time.
func newGuardedHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("parse dial address %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("non-ip dial address %q", host)
			}
			if blocked, reason := isBlockedIP(ip); blocked {
				return fmt.Errorf("egress to %s blocked: %s", host, reason)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			DisableKeepAlives:     true,
		},
	}
}

// hashRequiredType reports whether a resource type MUST carry a stored content
// hash to be verifiable (§G21): official documentation and code references.
func hashRequiredType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "official-doc", "official_doc", "code":
		return true
	}
	return false
}
