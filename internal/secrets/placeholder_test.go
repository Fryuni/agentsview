package secrets

import (
	"strings"
	"testing"
)

// TestRejectsAWSDocsPlaceholders pins the new aws-access-key validator: every
// AKIA/ASIA key that AWS itself documents as the example value, plus the
// EXAMPL[0-9] variants used in test fixtures, must be skipped. These are the
// values that flood agentsview's stored findings on any session that
// discusses IAM or scans a sample, and they are the user-reported false
// positives the validator exists to eliminate.
func TestRejectsAWSDocsPlaceholders(t *testing.T) {
	placeholders := []string{
		// AWS canonical example from the IAM docs.
		"AKIAIOSFODNN7EXAMPLE",
		// EXAMPL[0-9] variants used in test fixtures across the ecosystem.
		"AKIAIOSFODNN7EXAMPL1",
		"AKIAIOSFODNN7EXAMPL2",
		"AKIAIOSFODNN7EXAM123",
		// ASIA (session) variant.
		"ASIAIOSFODNN7EXAMPLE",
	}
	for _, p := range placeholders {
		t.Run(p, func(t *testing.T) {
			text := "key=" + p + " end"
			for _, m := range Scan(text) {
				if m.Rule == "aws-access-key" {
					t.Errorf("aws-access-key matched placeholder %q (mask=%q)",
						p, m.Redacted)
				}
			}
		})
	}
}

// TestAcceptsRealAWSKeys confirms the filter does not over-reach: an AKIA key
// with a body that contains neither EXAMPL nor IOSFODNN must still match.
func TestAcceptsRealAWSKeys(t *testing.T) {
	realLooking := []string{
		"AKIA1234567890ABCDEF",
		"AKIAZYXWVUTSRQPONMLK",
		"ASIAQWERTYUIOPASDFGH",
	}
	for _, k := range realLooking {
		t.Run(k, func(t *testing.T) {
			text := "key=" + k + " end"
			found := false
			for _, m := range Scan(text) {
				if m.Rule == "aws-access-key" {
					found = true
				}
			}
			if !found {
				t.Errorf("aws-access-key did not match real-looking key %q", k)
			}
		})
	}
}

// TestRejectsAnthropicPlaceholders pins the anthropic-key validator: keys
// whose final four characters repeat are dropped. These dominate
// agentsview's own historical findings (sk-ant-...AAAA, ...BBBB) and the
// public test corpora.
func TestRejectsAnthropicPlaceholders(t *testing.T) {
	placeholders := []string{
		"sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAA",
		"sk-ant-api03-BBBBBBBBBBBBBBBBBBBBBB",
		"sk-ant-api03-CCCCCCCCCCCCCCCCCCCCCC",
		"sk-ant-api03-" + strings.Repeat("Z", 24),
		"sk-ant-api03-XyZ1aB2cD3eF4gH5iJ60000",
	}
	for _, p := range placeholders {
		t.Run(p, func(t *testing.T) {
			text := "TOKEN=" + p + " end"
			for _, m := range Scan(text) {
				if m.Rule == "anthropic-key" {
					t.Errorf("anthropic-key matched placeholder %q (mask=%q)",
						p, m.Redacted)
				}
			}
		})
	}
}

// TestAcceptsRealAnthropicKeys confirms the suffix-repeat filter does not
// drop high-entropy keys that happen to share a single trailing character.
func TestAcceptsRealAnthropicKeys(t *testing.T) {
	realLooking := []string{
		"sk-ant-api03-Xa9Kd03Lm5Qp7Rt2Vw8Zb4",
		"sk-ant-api03-Nc6Mp1Hj9Bg3Tf5Ds8Lr0E",
		// Only the last char repeats; not enough to trigger the filter.
		"sk-ant-api03-Xa9Kd03Lm5Qp7Rt2Vw8ZbE",
	}
	for _, k := range realLooking {
		t.Run(k, func(t *testing.T) {
			text := "TOKEN=" + k + " end"
			found := false
			for _, m := range Scan(text) {
				if m.Rule == "anthropic-key" {
					found = true
				}
			}
			if !found {
				t.Errorf("anthropic-key did not match real-looking key %q", k)
			}
		})
	}
}

// TestRejectsSlackPlaceholders pins the slack-token validator: tokens
// ending in the "0123" canonical fake suffix or a 4-character repeat are
// dropped. The "0123" suffix is the Slack docs convention for fake tokens
// and the dominant noise pattern in agentsview's stored findings.
func TestRejectsSlackPlaceholders(t *testing.T) {
	placeholders := []string{
		"xoxb-123456789012-abcdefABCDEF0123",
		"xoxs-123456789012-abcdefABCDEF0123",
		"xoxr-123456789012-abcdefABCDEF0123",
		"xoxb-123456789012-abcdefABCDEAAAA",
	}
	for _, p := range placeholders {
		t.Run(p, func(t *testing.T) {
			text := "TOKEN=" + p + " end"
			for _, m := range Scan(text) {
				if m.Rule == "slack-token" {
					t.Errorf("slack-token matched placeholder %q (mask=%q)",
						p, m.Redacted)
				}
			}
		})
	}
}

// TestAcceptsRealSlackTokens confirms the filter does not drop tokens
// whose tail is high-entropy.
func TestAcceptsRealSlackTokens(t *testing.T) {
	realLooking := []string{
		"xoxb-123456789012-abcdefABCDEFc8Jp",
		"xoxs-987654321098-fedcbaFEDCBAxYz9",
	}
	for _, k := range realLooking {
		t.Run(k, func(t *testing.T) {
			text := "TOKEN=" + k + " end"
			found := false
			for _, m := range Scan(text) {
				if m.Rule == "slack-token" {
					found = true
				}
			}
			if !found {
				t.Errorf("slack-token did not match real-looking token %q", k)
			}
		})
	}
}

// TestRejectsTrivialPEMBodies pins the private-key-block validator: PEM
// blocks whose body is too short to plausibly contain real key material are
// skipped. Agents emit these as illustrative examples ("BEGIN ... key bytes
// here ... END") and they were producing definite findings on hundreds of
// sessions.
func TestRejectsTrivialPEMBodies(t *testing.T) {
	cases := []string{
		"-----BEGIN RSA PRIVATE KEY-----\nMIIBjunk\n-----END RSA PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----\n<key bytes here>\n-----END PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----\nshort\n-----END EC PRIVATE KEY-----",
	}
	for _, p := range cases {
		t.Run(p[:40], func(t *testing.T) {
			for _, m := range Scan(p) {
				if m.Rule == "private-key-block" {
					t.Errorf("private-key-block matched trivial body %q", p)
				}
			}
		})
	}
}

// TestAcceptsRealisticPEMBodies confirms the body-length gate lets through
// PEM blocks with body lengths typical of real key material.
func TestAcceptsRealisticPEMBodies(t *testing.T) {
	body := strings.Repeat("MIIBSECRETKEYMATERIAL0123456789ABCDEF\n", 5)
	for _, label := range []string{"RSA", "EC", ""} {
		header := "PRIVATE KEY"
		if label != "" {
			header = label + " PRIVATE KEY"
		}
		t.Run(header, func(t *testing.T) {
			text := "-----BEGIN " + header + "-----\n" + body +
				"-----END " + header + "-----"
			found := false
			for _, m := range Scan(text) {
				if m.Rule == "private-key-block" {
					found = true
				}
			}
			if !found {
				t.Errorf("private-key-block did not match realistic PEM (%s)", header)
			}
		})
	}
}
