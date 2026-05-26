package secrets

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefiniteRules(t *testing.T) {
	cases := []struct {
		name string
		rule string
		text string
		want bool
	}{
		{"github classic", "github-pat",
			"tok ghp_8Hk3Wn7Dz4Rp2Vx9Mb6Tj0Qc5Lm1Yp8Bv4Hg x", true},
		{"github fine-grained", "github-pat",
			"github_pat_8Hk3Wn7Dz4Rp2Vx9Mb6Tj0Qc5Lm1Yp8Bv4HgN_X2cWp9", true},
		{"slack bot", "slack-token",
			"xoxb-549271836401-fHk7Bm3Pz9Wt5Vx2Yq8Nc", true},
		{"stripe live", "stripe-secret",
			"sk_live_7Qh3Wn8Dk4Rp9Vx2Mb6Tj0Qc5Lm", true},
		{"google api", "google-api-key",
			"AIza7Qh3Wn8Dk4Rp9Vx2Mb6Tj0Qc5Lm1Yp8Bv4H", true},
		{"google api ending dash", "google-api-key",
			"key AIza7Qh3Wn8Dk4Rp9Vx2Mb6Tj0Qc5Lm1Yp8Bv4- end", true},
		{"pem block", "private-key-block",
			"-----BEGIN RSA PRIVATE KEY-----\n" +
				rep("MIIBSECRETKEYMATERIAL0123456789ABCDEF\n", 5) +
				"-----END RSA PRIVATE KEY-----", true},
		{"plain prose", "", "the quick brown fox jumps over", false},
		{"short ghp", "", "ghp_tooShort", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scan(c.text)
			found := ""
			for _, m := range got {
				if m.Rule == c.rule {
					found = m.Rule
				}
			}
			if c.want {
				assert.NotEmpty(t, found,
					"expected rule %q to match %q; got %+v", c.rule, c.text, got)
			} else {
				assert.Empty(t, got,
					"expected no match for %q; got %+v", c.text, got)
			}
		})
	}
}

func TestCandidateRules(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dumm_Sig-Value12345"
	cases := []struct {
		name string
		rule string
		text string
		want bool
	}{
		{"jwt", "jwt", "auth: " + jwt, true},
		{"high entropy assignment", "high-entropy-assignment",
			"SECRET=Xa9Kd03Lm5Qp7Rt2Vw8Zb4Nc6", true},
		{"low entropy assignment", "high-entropy-assignment",
			"NAME=aaaaaaaaaaaaaaaaaaaa", false},
		{"short assignment", "high-entropy-assignment", "X=ab12", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scan(c.text)
			found := false
			for _, m := range got {
				if m.Rule == c.rule {
					found = true
					assert.Equal(t, ConfidenceCandidate, m.Confidence,
						"%s confidence", c.rule)
				}
			}
			assert.Equal(t, c.want, found,
				"rule %q match=%v want=%v for %q (got %+v)",
				c.rule, found, c.want, c.text, got)
		})
	}
}

// TestScanDefiniteReturnsOnlyDefinite confirms the inline-sync scan path
// reports definite vendor formats and skips the FP-prone candidate heuristics
// (high-entropy assignments, JWTs, basic-auth URLs) entirely.
func TestScanDefiniteReturnsOnlyDefinite(t *testing.T) {
	// One definite AWS key and one candidate high-entropy assignment.
	text := "aws AKIA7QHWN2DKR4FYPLJM and SECRET=Xa9Kd03Lm5Qp7Rt2Vw8Zb4Nc6"
	full := Scan(text)
	require.Len(t, full, 2,
		"precondition: Scan should report 2 matches (1 definite, 1 candidate)")
	got := ScanDefinite(text)
	require.Len(t, got, 1)
	assert.Equal(t, "aws-access-key", got[0].Rule)
	for _, m := range got {
		assert.Equal(t, ConfidenceDefinite, m.Confidence,
			"ScanDefinite returned non-definite match: %+v", m)
	}
}

// TestScanDefiniteMatchesScanDefiniteSubset confirms ScanDefinite yields the
// same spans (rule, offsets, redaction) that Scan reports for definite rules,
// so findings stored by the inline path and the full scan stay consistent.
func TestScanDefiniteMatchesScanDefiniteSubset(t *testing.T) {
	text := "key AKIA7QHWN2DKR4FYPLJM tok ghp_8Hk3Wn7Dz4Rp2Vx9Mb6Tj0Qc5Lm1Yp8Bv4Hg" +
		" SECRET=Xa9Kd03Lm5Qp7Rt2Vw8Zb4Nc6"
	var wantDef []Match
	for _, m := range Scan(text) {
		if m.Confidence == ConfidenceDefinite {
			wantDef = append(wantDef, m)
		}
	}
	got := ScanDefinite(text)
	require.Len(t, got, len(wantDef),
		"ScanDefinite count vs Scan definite count (%+v vs %+v)", got, wantDef)
	for i := range got {
		assert.Equal(t, wantDef[i].Rule, got[i].Rule, "match %d rule differs", i)
		assert.Equal(t, wantDef[i].Start, got[i].Start, "match %d start differs", i)
		assert.Equal(t, wantDef[i].End, got[i].End, "match %d end differs", i)
		assert.Equal(t, wantDef[i].Redacted, got[i].Redacted, "match %d redacted differs", i)
	}
}

// TestDefiniteRulesVersionDistinctFromFull pins the split-versioning contract:
// the inline definite-only scan stamps a version that differs from the full
// ruleset version, so secrets scan --backfill (which treats RulesVersion as
// current) re-scans inline-only sessions to pick up candidate findings.
func TestDefiniteRulesVersionDistinctFromFull(t *testing.T) {
	def := DefiniteRulesVersion()
	full := RulesVersion()
	require.NotEqual(t, full, def,
		"DefiniteRulesVersion must differ from RulesVersion (both %q)", def)
	require.NotEmpty(t, def, "versions must be non-empty")
	require.NotEmpty(t, full, "versions must be non-empty")
	assert.Equal(t, def, DefiniteRulesVersion(), "DefiniteRulesVersion not stable across calls")
	assert.Len(t, def, 64, "DefiniteRulesVersion length: %q", def)
	for _, c := range def {
		require.True(t, isLowerHex(c),
			"DefiniteRulesVersion has non-hex char %q in %q", c, def)
	}
}

func TestRulesVersionStableAndHex(t *testing.T) {
	v1 := RulesVersion()
	v2 := RulesVersion()
	require.Equal(t, v1, v2, "RulesVersion not stable")
	require.Len(t, v1, 64, "RulesVersion length: %q", v1) // sha256 hex
	for _, c := range v1 {
		require.True(t, isLowerHex(c),
			"RulesVersion has non-hex char %q in %q", c, v1)
	}
}

func TestVerify(t *testing.T) {
	// Non-grouped rule: the stored span is the full regex match.
	awsSrc := "export KEY=AKIA7QHWN2DKR4FYPLJM done"
	s := strings.Index(awsSrc, "AKIA")
	e := s + len("AKIA7QHWN2DKR4FYPLJM")
	assert.True(t, Verify("aws-access-key", awsSrc, s, e),
		"Verify should accept a valid AWS key at its coordinates")
	assert.False(t, Verify("aws-access-key", awsSrc, 0, 6),
		"Verify should reject coordinates that are not the key")
	assert.False(t, Verify("nonexistent-rule", awsSrc, s, e),
		"Verify should reject an unknown rule")
	assert.False(t, Verify("aws-access-key", awsSrc, s, len(awsSrc)+10),
		"Verify should reject out-of-bounds coordinates")
	// Grouped rule: the stored span is the captured group (the password),
	// not the full URL match. Verify must still accept it.
	urlSrc := "db=postgres://user:s3cretP4ss@host:5432/db"
	ps := strings.Index(urlSrc, "s3cretP4ss")
	pe := ps + len("s3cretP4ss")
	assert.True(t, Verify("basic-auth-url", urlSrc, ps, pe),
		"Verify should accept a grouped finding at its group coordinates")
}

// TestVerifyDetectsChangedSource locks in the core --reveal guarantee: a scan
// produces coordinates, Verify accepts them on the unchanged source, and
// rejects them once the bytes at those coordinates are no longer the secret.
func TestVerifyDetectsChangedSource(t *testing.T) {
	source := "export AWS=AKIA7QHWN2DKR4FYPLJM"
	// Seed from canonical Scan (what produces findings and what Verify uses).
	matches := Scan(source)
	require.NotEmpty(t, matches, "expected at least one match in source")
	m := matches[0]
	assert.True(t, Verify(m.Rule, source, m.Start, m.End),
		"Verify should accept unchanged source at [%d,%d)", m.Start, m.End)
	// Same length, but the secret at [Start,End) is replaced by a zero-entropy
	// run that matches no rule, so Verify must reject the stale coordinates.
	changed := source[:m.Start] + strings.Repeat("X", m.End-m.Start)
	assert.False(t, Verify(m.Rule, changed, m.Start, m.End),
		"Verify should reject when the source changed at those coords")
}

// TestVerifyRejectsSuppressedCandidate ensures Verify mirrors canonical Scan,
// not raw scanning: a candidate that overlaps a definite is suppressed by Scan,
// so Verify must reject its coordinates even though scanRaw reports it.
func TestVerifyRejectsSuppressedCandidate(t *testing.T) {
	src := "https://user:sk-ant-api03-Xa9Kd03Lm5Qp7Rt2Vw8Zb4@example.com"
	var cand Match
	for _, m := range scanRaw(src) {
		if m.Rule == "basic-auth-url" {
			cand = m
			break
		}
	}
	require.NotEmpty(t, cand.Rule,
		"precondition: scanRaw should report a basic-auth-url candidate")
	assert.False(t, Verify("basic-auth-url", src, cand.Start, cand.End),
		"Verify must reject a candidate that canonical Scan suppresses")
}

// isLowerHex reports whether c is a lowercase hexadecimal digit, the alphabet
// a SHA-256 hex digest is built from.
func isLowerHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// rep returns s repeated n times (test helper for building token bodies).
func rep(s string, n int) string {
	var out strings.Builder
	for range n {
		out.WriteString(s)
	}
	return out.String()
}

// TestHasRepeatingBlock pins the seed-pattern detector that catches
// placeholders built by repeating a short string. Block size 1 is the
// "ghp_aaaa…" shape; sizes 2..6 cover "A1b2A1b2…", "aB3_xaB3_x…", etc.
func TestHasRepeatingBlock(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"single byte dominating", strings.Repeat("a", 36), true},
		{"block size 4 A1b2", strings.Repeat("A1b2", 20), true},
		{"block size 4 a1B2", strings.Repeat("a1B2", 8), true},
		{"block size 5 aB3_x", strings.Repeat("aB3_x", 7), true},
		{"block size 2", strings.Repeat("xy", 10), true},
		{"random body", "7Qh3Wn8Dk4Rp9Vx2Mb6Tj0Qc5Lm", false},
		{"random aws body", "7QHWN2DKR4FYPLJM", false},
		{"random pat body", "8Hk3Wn7Dz4Rp2Vx9Mb6Tj0Qc5Lm1Yp8Bv4Hg", false},
		{"too short", "abcd", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hasRepeatingBlock(c.s))
		})
	}
}

// TestHasMonotoneRun pins the alphabet/digit-run detector that catches
// placeholders built from sequential ASCII ("abcdef", "1234567890",
// "ZYXWVU"). The 6-char minimum is small enough to catch the dominant
// noise shapes without flagging random secrets that happen to include a
// short run by chance.
func TestHasMonotoneRun(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"abcdef", "abcdef", true},
		{"1234567890", "1234567890", true},
		{"ZYXWVU", "ZYXWVU", true},
		{"fedcba", "fedcba", true},
		{"abcde (only 5)", "abcde", false},
		{"random", "7Qh3Wn8Dk4Rp9Vx2Mb6Tj0Qc5Lm", false},
		{"isolated +1 transitions", "549271836401", false},
		{"embedded run", "Xabcdef9", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hasMonotoneRun(c.s, 6))
		})
	}
}
