package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// rule is one secret detector. group selects a capture group as the
// reported span (0 = whole match); validate optionally gates a match
// (nil = always keep); mask renders the persisted/displayed redaction.
type rule struct {
	name       string
	confidence string
	prefilters []string // literal anchors; empty => always scan
	re         *regexp.Regexp
	group      int
	validate   func(string) bool
	mask       func(string) string
}

var rules = []rule{
	{
		name:       "aws-access-key",
		confidence: ConfidenceDefinite,
		prefilters: []string{"AKIA", "ASIA"},
		re:         regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		validate:   notAWSDocsPlaceholder,
		mask:       func(s string) string { return maskKeepEnds(s, 4, 4) },
	},
	{
		name:       "anthropic-key",
		confidence: ConfidenceDefinite,
		prefilters: []string{"sk-ant-"},
		re:         regexp.MustCompile(`\bsk-ant-[0-9A-Za-z][0-9A-Za-z_\-]{18,}`),
		validate:   notLowEntropySuffix,
		mask:       func(s string) string { return maskKeepEnds(s, 7, 4) },
	},
	{
		name:       "basic-auth-url",
		confidence: ConfidenceCandidate,
		prefilters: []string{"://"},
		re:         regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@]+:([^\s:/@]+)@[^\s/]+`),
		group:      1, // mask the password, not the whole URL
		mask:       func(s string) string { return maskKeepEnds(s, 0, 0) },
	},
	{
		name:       "github-pat",
		confidence: ConfidenceDefinite,
		prefilters: []string{"ghp_", "github_pat_"},
		re:         regexp.MustCompile(`\b(?:ghp_[0-9A-Za-z]{36}|github_pat_[0-9A-Za-z_]{40,})\b`),
		mask:       func(s string) string { return maskKeepEnds(s, 4, 4) },
	},
	{
		name:       "slack-token",
		confidence: ConfidenceDefinite,
		prefilters: []string{"xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-"},
		re:         regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z]{10,}(?:-[0-9A-Za-z]+)*`),
		validate:   notSlackTokenPlaceholder,
		mask:       func(s string) string { return maskKeepEnds(s, 5, 4) },
	},
	{
		name:       "stripe-secret",
		confidence: ConfidenceDefinite,
		prefilters: []string{"sk_live_", "rk_live_"},
		re:         regexp.MustCompile(`\b[sr]k_live_[0-9A-Za-z]{16,}\b`),
		mask:       func(s string) string { return maskKeepEnds(s, 8, 4) },
	},
	{
		name:       "google-api-key",
		confidence: ConfidenceDefinite,
		prefilters: []string{"AIza"},
		// Capture the key in group 1 and require a non-body terminator (or
		// end of text) rather than a trailing \b, so keys ending in '-'
		// still match instead of being silently skipped.
		re:    regexp.MustCompile(`\b(AIza[0-9A-Za-z_\-]{35})(?:[^0-9A-Za-z_\-]|$)`),
		group: 1,
		mask:  func(s string) string { return maskKeepEnds(s, 4, 4) },
	},
	{
		name:       "private-key-block",
		confidence: ConfidenceDefinite,
		prefilters: []string{"-----BEGIN"},
		re: regexp.MustCompile(
			`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
		validate: notTrivialPEMBody,
		mask:     func(string) string { return "[redacted private key block]" },
	},
	{
		name:       "jwt",
		confidence: ConfidenceCandidate,
		prefilters: []string{"eyJ"},
		re: regexp.MustCompile(
			`\beyJ[0-9A-Za-z_\-]+\.[0-9A-Za-z_\-]+\.[0-9A-Za-z_\-]+`),
		mask: func(s string) string { return maskKeepEnds(s, 3, 0) },
	},
	{
		// Known false positives: filesystem paths and URLs can clear the
		// entropy gate because the value charset includes '/'. Accepted for a
		// candidate rule; do not lower the threshold to chase these.
		name:       "high-entropy-assignment",
		confidence: ConfidenceCandidate,
		prefilters: []string{"=", ":"},
		re: regexp.MustCompile(
			`(?i)\b[a-z][a-z0-9_]{2,}\s*[=:]\s*['"]?([A-Za-z0-9+/_\-]{20,})['"]?`),
		group:    1,
		validate: highEntropyValue,
		mask:     func(s string) string { return maskKeepEnds(s, 0, 4) },
	},
}

// definiteRules is the well-anchored vendor-format subset of rules, computed
// once at load. ScanDefinite uses it for the fast inline-sync path.
var definiteRules = filterByConfidence(rules, ConfidenceDefinite)

func filterByConfidence(rs []rule, confidence string) []rule {
	out := make([]rule, 0, len(rs))
	for _, r := range rs {
		if r.confidence == confidence {
			out = append(out, r)
		}
	}
	return out
}

// shannonEntropy returns the per-byte Shannon entropy (bits) of s.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// highEntropyValue gates the high-entropy-assignment rule: the value must
// be long enough and random-looking to plausibly be a secret.
func highEntropyValue(s string) bool {
	return len(s) >= 20 && shannonEntropy(s) >= 3.5
}

// notAWSDocsPlaceholder rejects access key IDs that look like the canonical
// AWS-documentation placeholders. AWS code samples consistently use the
// synthetic body "IOSFODNN7EXAMPLE" (plus EXAMPL[0-9] variants in test
// fixtures), and those values overwhelmingly leak into agent transcripts
// when a developer asks an AI to discuss IAM, write a snippet, or scan
// fixture files. Both markers are 6–8 characters long, so the chance of a
// real 16-char body containing them is astronomical (≈3.5e-13 for IOSFODNN
// in random uppercase alphanumerics) — well below the false-negative
// threshold for a definite rule.
func notAWSDocsPlaceholder(s string) bool {
	return !strings.Contains(s, "EXAMPL") && !strings.Contains(s, "IOSFODNN")
}

// notLowEntropySuffix rejects Anthropic API keys whose final four characters
// are a single repeated rune (sk-ant-...AAAA, ...BBBB, ...0000). Real keys
// are base62-ish random; a 4-rune repeat at the very end has roughly 1 / 62³
// ≈ 4e-6 odds of occurring naturally, which is acceptable false-negative
// loss for a rule that otherwise produces noise on every transcript that
// quotes a doc placeholder.
func notLowEntropySuffix(s string) bool {
	if len(s) < 4 {
		return true
	}
	last := s[len(s)-1]
	for i := len(s) - 2; i >= len(s)-4; i-- {
		if s[i] != last {
			return true
		}
	}
	return false
}

// notSlackTokenPlaceholder rejects Slack tokens that look like the
// well-known placeholder forms used in the Slack docs and most public
// secrets-scanner test corpora: anything ending in the literal "0123"
// (the canonical fake suffix) or whose final four characters repeat. A
// real token cannot end on "0123" or a 4-char repeat with meaningful
// probability — base62 odds are roughly (1/62)^4 ≈ 7e-8 per real key.
func notSlackTokenPlaceholder(s string) bool {
	if strings.HasSuffix(s, "0123") {
		return false
	}
	return notLowEntropySuffix(s)
}

// notTrivialPEMBody rejects PEM blocks whose body is too short to plausibly
// hold a real private key. The shortest real key material (Ed25519,
// PKCS#8-wrapped) is ~256 base64 characters; this gate requires 150
// non-whitespace bytes between the BEGIN and END markers, which lets in
// every real format while filtering placeholders like
// "-----BEGIN PRIVATE KEY-----\nMIIBjunk\n-----END PRIVATE KEY-----" that
// agents emit as illustrative examples.
func notTrivialPEMBody(s string) bool {
	const minBody = 150
	// Skip past the first "-----BEGIN ... -----" marker.
	const beginMarker = "-----"
	first := strings.Index(s, beginMarker)
	if first < 0 {
		return true
	}
	headerEnd := strings.Index(s[first+len(beginMarker):], beginMarker)
	if headerEnd < 0 {
		return true
	}
	bodyStart := first + len(beginMarker) + headerEnd + len(beginMarker)
	endIdx := strings.LastIndex(s, "-----END")
	if endIdx <= bodyStart {
		return true
	}
	n := 0
	for _, c := range s[bodyStart:endIdx] {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			n++
			if n >= minBody {
				return true
			}
		}
	}
	return false
}

// rulesAlgorithmVersion bumps when matching *logic* changes (e.g. a new
// validate gate). Pattern edits are detected automatically because the
// regexes are folded into the hash; validate functions are not, so adding
// a new gate must bump this constant by hand. Mirrors db.ClassifierHash.
//
// v2: added notAWSDocsPlaceholder, notLowEntropySuffix, notTrivialPEMBody,
// and notSlackTokenPlaceholder validators to drop canonical doc-placeholder
// findings (AKIA*EXAMPL*, sk-ant-…AAAA, trivial PEM bodies, xoxb-…0123).
const rulesAlgorithmVersion = 2

// Verify reports whether the named rule still produces a finding at exactly
// [start:end) within source. Used by --reveal to confirm a stored finding's
// coordinates still resolve to the same secret before printing a full value.
// It re-runs the canonical Scan (the same function that produces findings, so
// overlap suppression is applied identically) and matches grouped rules by
// their captured-group span.
func Verify(ruleName, source string, start, end int) bool {
	if start < 0 || end > len(source) || start >= end {
		return false
	}
	for _, m := range Scan(source) {
		if m.Rule == ruleName && m.Start == start && m.End == end {
			return true
		}
	}
	return false
}

// RulesVersion is a stable hex SHA-256 over the algorithm version and the full
// ruleset (names, confidences, regexes, prefilters). It is the version a full
// Scan stamps. It changes when the ruleset changes, so persisted findings can
// be invalidated and rescanned.
func RulesVersion() string {
	return rulesVersion("full", rules)
}

// DefiniteRulesVersion is the version stamped by the definite-only inline-sync
// scan. It is deliberately distinct from RulesVersion (a "definite" scope tag,
// plus a hash over only the definite rules) so secrets scan --backfill — which
// treats RulesVersion as current — re-scans sessions that received only the
// fast inline scan, letting an explicit scan add candidate findings. A later
// inline resync re-stamps this version, dropping those candidates by design.
func DefiniteRulesVersion() string {
	return rulesVersion("definite", definiteRules)
}

// rulesVersion hashes the algorithm version, a scope tag, and the given rules.
// The scope tag guarantees the full and definite versions never collide even
// if their rule lists were identical.
func rulesVersion(scope string, rs []rule) string {
	h := sha256.New()
	fmt.Fprintf(h, "v%d\n%s\n", rulesAlgorithmVersion, scope)
	for i := range rs {
		r := &rs[i]
		fmt.Fprintf(h, "R\x1f%s\x1f%s\x1f%d\x1f%s\n",
			r.name, r.confidence, r.group, r.re.String())
		for _, p := range r.prefilters {
			fmt.Fprintf(h, "P\x1f%s\n", p)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
