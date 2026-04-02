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
