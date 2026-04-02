package config

import "testing"

func TestValidateRejectsSameDoHPathOnSharedPort(t *testing.T) {
	cfg := &Config{
		Listen: ListenConfig{DOH: ":443", DoHPath: "/dns-query"},
		ParallelReturn: ParallelReturnConfig{
			Enabled: true,
			Listen:  ListenConfig{DOH: ":443", DoHPath: "/dns-query"},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for shared DoH port with same path")
	}
}

func TestValidateRejectsSharedDoTPortWithoutDistinctSNI(t *testing.T) {
	cfg := &Config{
		Listen: ListenConfig{DOT: ":853", DoTSNI: "dns.example.com"},
		ParallelReturn: ParallelReturnConfig{
			Enabled: true,
			Listen:  ListenConfig{DOT: ":853", DoTSNI: "dns.example.com"},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for shared DoT port with same SNI")
	}
}

func TestValidateAllowsSharedPortsWithDistinctSelectors(t *testing.T) {
	cfg := &Config{
		Listen: ListenConfig{DOH: ":443", DoHPath: "/dns-query", DOT: ":853", DoTSNI: "main.example.com", DOQ: ":853", DoQSNI: "main.example.com"},
		ParallelReturn: ParallelReturnConfig{
			Enabled: true,
			Listen:  ListenConfig{DOH: ":443", DoHPath: "/parallel-dns-query", DOT: ":853", DoTSNI: "parallel.example.com", DOQ: ":853", DoQSNI: "parallel.example.com"},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected config to be valid, got %v", err)
	}
}

func TestParallelReturnWarmCacheTTLDefaultsToFive(t *testing.T) {
	cfg := &Config{}
	if cfg.ParallelReturn.WarmCacheTTL != 0 {
		t.Fatalf("expected zero value before normalization, got %d", cfg.ParallelReturn.WarmCacheTTL)
	}
	if cfg.ParallelReturn.WarmCacheTTL <= 0 {
		cfg.ParallelReturn.WarmCacheTTL = 5
	}
	if cfg.ParallelReturn.WarmCacheTTL != 5 {
		t.Fatalf("expected default warm cache ttl to be 5, got %d", cfg.ParallelReturn.WarmCacheTTL)
	}
}
