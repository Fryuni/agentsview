package pricing

import (
	"math"
	"testing"
)

func TestParseLiteLLMPricing(t *testing.T) {
	data := []byte(`{
		"sample_spec": {"max_tokens": 4096},
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_creation_input_token_cost": 0.00000375,
			"cache_read_input_token_cost": 0.0000003,
			"litellm_provider": "anthropic"
		}
	}`)

	prices, err := ParseLiteLLMPricing(data)
	if err != nil {
		t.Fatalf("ParseLiteLLMPricing: %v", err)
	}

	var found *ModelPricing
	for i := range prices {
		if prices[i].ModelPattern == "claude-sonnet-4-20250514" {
			found = &prices[i]
			break
		}
	}
	if found == nil {
		t.Fatal("claude-sonnet-4-20250514 not found in results")
	}

	assertClose(t, "InputPerMTok", found.InputPerMTok, 3.0)
	assertClose(t, "OutputPerMTok", found.OutputPerMTok, 15.0)
	assertClose(t, "CacheCreationPerMTok",
		found.CacheCreationPerMTok, 3.75)
	assertClose(t, "CacheReadPerMTok",
		found.CacheReadPerMTok, 0.30)
}

func TestParseLiteLLMPricingMultipleProviders(t *testing.T) {
	data := []byte(`{
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"litellm_provider": "anthropic"
		},
		"gpt-4o": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.00001,
			"litellm_provider": "openai"
		}
	}`)

	prices, err := ParseLiteLLMPricing(data)
	if err != nil {
		t.Fatalf("ParseLiteLLMPricing: %v", err)
	}

	foundAnthropic := false
	foundOpenAI := false
	for _, p := range prices {
		switch p.ModelPattern {
		case "claude-sonnet-4-20250514":
			foundAnthropic = true
		case "gpt-4o":
			foundOpenAI = true
		}
	}
	if !foundAnthropic {
		t.Error("anthropic model not found")
	}
	if !foundOpenAI {
		t.Error("openai model not found")
	}
}

func TestParseLiteLLMPricingSkipsNoCost(t *testing.T) {
	data := []byte(`{
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"litellm_provider": "anthropic"
		},
		"no-cost-model": {
			"litellm_provider": "anthropic"
		}
	}`)

	prices, err := ParseLiteLLMPricing(data)
	if err != nil {
		t.Fatalf("ParseLiteLLMPricing: %v", err)
	}

	if len(prices) != 1 {
		t.Fatalf("expected 1 model, got %d", len(prices))
	}
	if prices[0].ModelPattern != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected model: %s", prices[0].ModelPattern)
	}
}

func TestFallbackPricing(t *testing.T) {
	prices := FallbackPricing()
	if len(prices) == 0 {
		t.Fatal("FallbackPricing returned empty")
	}

	required := map[string]bool{
		"claude-sonnet-4-20250514":  false,
		"claude-opus-4-20250514":    false,
		"claude-haiku-3-5-20241022": false,
	}
	for _, p := range prices {
		if _, ok := required[p.ModelPattern]; ok {
			required[p.ModelPattern] = true
		}
	}
	for model, found := range required {
		if !found {
			t.Errorf("required model %s missing", model)
		}
	}
}

func assertClose(
	t *testing.T,
	name string,
	got, want float64,
) {
	t.Helper()
	if math.Abs(got-want) > 0.001 {
		t.Errorf("%s: got %f, want %f", name, got, want)
	}
}
