package secrets

import (
	"fmt"
	"strings"
	"testing"
)

func TestScanFindsAWSAccessKey(t *testing.T) {
	text := "export AWS_KEY=AKIA1234567890ABCDEF then continue"
	got := Scan(text)
	if len(got) != 1 {
		t.Fatalf("Scan returned %d matches, want 1: %+v", len(got), got)
	}
	m := got[0]
	if m.Rule != "aws-access-key" {
		t.Errorf("Rule = %q, want aws-access-key", m.Rule)
	}
	if m.Confidence != ConfidenceDefinite {
		t.Errorf("Confidence = %q, want definite", m.Confidence)
	}
	if text[m.Start:m.End] != "AKIA1234567890ABCDEF" {
		t.Errorf("span = %q, want AKIA1234567890ABCDEF", text[m.Start:m.End])
	}
	if m.Index != 0 {
		t.Errorf("Index = %d, want 0", m.Index)
	}
}

func TestScanNoMatch(t *testing.T) {
	if got := Scan("just some ordinary prose with no secrets"); len(got) != 0 {
		t.Fatalf("Scan returned %d matches, want 0: %+v", len(got), got)
	}
}

func TestRedactMasksSecretButKeepsContext(t *testing.T) {
	text := "export AWS_KEY=AKIA1234567890ABCDEF then continue"
	got := Redact(text)
	if strings.Contains(got, "AKIA1234567890ABCDEF") {
		t.Fatalf("Redact leaked the full secret: %q", got)
	}
	if !strings.HasPrefix(got, "export AWS_KEY=") {
		t.Errorf("Redact dropped surrounding context: %q", got)
	}
	if !strings.HasSuffix(got, " then continue") {
		t.Errorf("Redact dropped trailing context: %q", got)
	}
	if !strings.Contains(got, "AKIA…CDEF") {
		t.Errorf("Redact did not use the masked form: %q", got)
	}
}

func TestRedactNoMatchReturnsInput(t *testing.T) {
	in := "nothing to see here"
	if got := Redact(in); got != in {
		t.Fatalf("Redact mutated clean text: %q", got)
	}
}

func TestScanSuppressesCandidateOverlappingDefinite(t *testing.T) {
	// The basic-auth-url candidate and anthropic-key definite both match
	// inside this URL; only the definite finding should be returned.
	text := "https://user:sk-ant-api03-Xa9Kd03Lm5Qp7Rt2Vw8Zb4@example.com"
	got := Scan(text)
	for _, m := range got {
		if m.Confidence == ConfidenceCandidate {
			t.Errorf("candidate %q not suppressed despite overlapping definite", m.Rule)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected at least the definite anthropic-key finding")
	}
}

func TestRedactMasksUnionIncludingCandidate(t *testing.T) {
	text := "TOKEN=sk-ant-api03-Nc6Mp1Hj9Bg3Tf5Ds8Lr0E end"
	got := Redact(text)
	if strings.Contains(got, "sk-ant-api03-Nc6Mp1Hj9Bg3Tf5Ds8Lr0E") {
		t.Fatalf("Redact leaked secret: %q", got)
	}
}

// TestRedactWindowMasksStraddlingSecret pins the content-search guarantee: a
// secret that extends past the snippet window is still masked. Redacting the
// truncated window directly would see only a fragment (here a PEM block missing
// its END line), fail to match any rule, and leak raw key bytes.
func TestRedactWindowMasksStraddlingSecret(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\n" +
		strings.Repeat("MIIBSECRETKEYMATERIAL0123456789ABCDEF\n", 5) +
		"-----END RSA PRIVATE KEY-----"
	full := "see attached key " + pem + " thanks"
	pemStart := strings.Index(full, "-----BEGIN")
	lo, hi := pemStart-5, pemStart+40 // overlaps the PEM but cuts before END

	// Hazard check: redacting the bare window leaks, because the fragment has
	// no END line for the private-key-block rule to anchor on.
	if naive := Redact(full[lo:hi]); !strings.Contains(naive, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("precondition: window should straddle the key; got %q", naive)
	}

	got := RedactWindow(full, lo, hi)
	if strings.Contains(got, "SECRETKEYMATERIAL") {
		t.Errorf("RedactWindow leaked straddling key material: %q", got)
	}
	if !strings.Contains(got, "[redacted private key block]") {
		t.Errorf("RedactWindow did not mask the key block: %q", got)
	}
}

// TestRedactWindowMasksStraddlingGroupedSecret covers grouped rules, whose
// reported span is a capture group (the password / the high-entropy value), not
// the full match. A window covering only the group must still mask it: redacting
// the bare slice would fail to re-detect (the "scheme://user:" or "key=" context
// is gone) and leak the secret.
func TestRedactWindowMasksStraddlingGroupedSecret(t *testing.T) {
	t.Run("high-entropy-assignment", func(t *testing.T) {
		val := "Xa9Kd03Lm5Qp7Rt2Vw8Zb4Nc6QrStUvWxYz0123"
		full := "export api_key=" + val + " done"
		vs := strings.Index(full, val)
		// Window starts inside the value, past the "api_key=" the rule needs.
		got := RedactWindow(full, vs+5, vs+15)
		if strings.Contains(got, val[5:20]) {
			t.Errorf("leaked high-entropy value fragment: %q", got)
		}
	})
	t.Run("basic-auth-url", func(t *testing.T) {
		pw := "Sup3rSecretP4ssw0rd"
		full := "db=postgres://user:" + pw + "@host:5432/app"
		ps := strings.Index(full, pw)
		// Window lands inside the password, past the "://user:" the rule needs.
		got := RedactWindow(full, ps+2, ps+8)
		if strings.Contains(got, pw[2:12]) {
			t.Errorf("leaked basic-auth password fragment: %q", got)
		}
	})
}

// TestRedactWindowKeepsContextAndContainedSecrets checks the common path: a
// secret fully inside the window keeps its rule mask, surrounding context
// survives, and a window with no secret is returned verbatim.
func TestRedactWindowKeepsContextAndContainedSecrets(t *testing.T) {
	full := "the key is AKIA1234567890ABCDEF in config"
	got := RedactWindow(full, 0, len(full))
	if strings.Contains(got, "AKIA1234567890ABCDEF") {
		t.Errorf("contained secret not masked: %q", got)
	}
	if !strings.Contains(got, "the key is ") || !strings.Contains(got, " in config") {
		t.Errorf("context not preserved: %q", got)
	}
	clean := "just some ordinary prose with no secrets at all"
	if got := RedactWindow(clean, 0, len(clean)); got != clean {
		t.Errorf("clean window altered: %q", got)
	}
}

func TestRedactNeverLeaksKnownSecrets(t *testing.T) {
	secrets := []string{
		"AKIA1234567890ABCDEF",
		"ghp_" + rep("a", 36),
		"xoxb-123456789012-abcdefABCDEFc8Jp",
		"xoxs-987654321098-fedcbaFEDCBAxYz9",
		"sk_live_" + rep("a1B2", 8),
		"AIza" + rep("aB3_x", 7),
		"AIza" + rep("aB3_x", 6) + "aB3_-",
		"sk-ant-api03-" + "ZyXwVuTsRqPoNmLkJiHgFe",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dumm_Sig-Value12345",
	}
	for _, sec := range secrets {
		for _, tmpl := range []string{"%s", "prefix %s suffix", "a=%s\nb=2"} {
			text := fmt.Sprintf(tmpl, sec)
			out := Redact(text)
			if strings.Contains(out, sec) {
				t.Errorf("Redact leaked %q in template %q -> %q", sec, tmpl, out)
			}
		}
	}
}

func TestScanRedactedNeverEqualsFullSecret(t *testing.T) {
	// One sample per rule, scanned in isolation, so every rule's masked form
	// is exercised. The private-key-block mask is a fixed string and trivially
	// differs from its (multi-line) match, so it is covered by the others.
	samples := []string{
		"k=AKIA1234567890ABCDEF",
		"tok ghp_" + rep("a", 36),
		"xoxb-123456789012-abcdefABCDEFc8Jp",
		"sk_live_" + rep("a1B2", 8),
		"AIza" + rep("aB3_x", 7),
		"sk-ant-api03-" + "QrStUvWxYz0987654321Ab",
		"auth: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dumm_Sig-Value12345",
		"https://user:supersecretpw@example.com",
		"SECRET=Xa9Kd03Lm5Qp7Rt2Vw8Zb4Nc6",
	}
	for _, text := range samples {
		matches := Scan(text)
		if len(matches) == 0 {
			t.Errorf("no matches for sample %q", text)
			continue
		}
		for _, m := range matches {
			full := text[m.Start:m.End]
			if m.Redacted == full {
				t.Errorf("Redacted equals full secret for rule %q: %q", m.Rule, full)
			}
		}
	}
}

func TestBasicAuthURLDetectsPasswordSpan(t *testing.T) {
	text := "db at postgres://admin:Sup3rSecretPw@db.example.com:5432/app"
	var m *Match
	for _, got := range Scan(text) {
		if got.Rule == "basic-auth-url" {
			m = &got
		}
	}
	if m == nil {
		t.Fatalf("expected a basic-auth-url candidate; got %+v", Scan(text))
	}
	if span := text[m.Start:m.End]; span != "Sup3rSecretPw" {
		t.Errorf("span = %q, want only the password Sup3rSecretPw", span)
	}
	if m.Confidence != ConfidenceCandidate {
		t.Errorf("Confidence = %q, want candidate", m.Confidence)
	}
	red := Redact(text)
	// Assert the exact fully-masked form: no password character survives
	// (this fails if the mask is loosened to reveal a suffix) while the
	// surrounding URL context is preserved.
	if !strings.Contains(red, "postgres://admin:…@db.example.com") {
		t.Errorf("Redact did not fully mask the password in context: %q", red)
	}
}

func TestScanJWTNotDuplicatedAsHighEntropy(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dumm_Sig-Value12345"
	got := Scan("auth: " + jwt)
	foundJWT := false
	for _, m := range got {
		if m.Rule == "high-entropy-assignment" {
			t.Errorf("JWT segment reported as high-entropy-assignment: %+v", got)
		}
		if m.Rule == "jwt" {
			foundJWT = true
		}
	}
	if !foundJWT {
		t.Errorf("expected a jwt candidate; got %+v", got)
	}
}
