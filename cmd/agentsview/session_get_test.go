package main

import (
	"bytes"
	"strings"
	"testing"

	"go.kenn.io/agentsview/internal/service"
)

func TestPrintSessionDetailShowsSecretLeak(t *testing.T) {
	d := &service.SessionDetail{}
	d.ID = "s1"
	d.SecretLeakCount = 3
	var buf bytes.Buffer
	if err := printSessionDetailHuman(&buf, d); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Secrets") || !strings.Contains(out, "3") {
		t.Errorf("detail output missing secret leak count:\n%s", out)
	}
}

func TestPrintSessionDetailHidesZeroSecretLeak(t *testing.T) {
	d := &service.SessionDetail{}
	d.ID = "s1"
	d.SecretLeakCount = 0
	var buf bytes.Buffer
	if err := printSessionDetailHuman(&buf, d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Secrets") {
		t.Errorf("clean session should not show a Secrets line:\n%s", buf.String())
	}
}
