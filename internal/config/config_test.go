package config

import "testing"

func TestApplyDefaultsAndValidate(t *testing.T) {
	cfg := Config{
		AcknowledgeAuthorization: true,
		Target:                   Target{Transport: "stdio", Command: []string{"server"}},
	}
	ApplyDefaults(&cfg)
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Intensity != "low" || cfg.Sandbox != "none" || cfg.Budgets.MaxRequests != 100 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	tests := []Config{
		{Target: Target{Transport: "stdio", Command: []string{"server"}}},
		{AcknowledgeAuthorization: true, Target: Target{Transport: "ftp", Command: []string{"server"}}},
		{AcknowledgeAuthorization: true, Target: Target{Transport: "stdio", Command: []string{"server"}}, Intensity: "extreme"},
		{AcknowledgeAuthorization: true, Target: Target{Transport: "stdio", Command: []string{"server"}}, Budgets: Budgets{MaxRequests: -1}},
	}
	for _, cfg := range tests {
		ApplyDefaults(&cfg)
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected validation error for %+v", cfg)
		}
	}
}
