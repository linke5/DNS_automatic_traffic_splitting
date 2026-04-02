package router

import (
	"context"
	"net"
	"testing"
	"time"

	"doh-autoproxy/internal/client"
	"doh-autoproxy/internal/config"

	"github.com/miekg/dns"
)

type fakeParallelClient struct {
	resp  *dns.Msg
	err   error
	delay time.Duration
}

func (f *fakeParallelClient) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.resp == nil {
		return nil, f.err
	}
	return f.resp.Copy(), f.err
}

func newAResp(name, ip string, ttl uint32) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeA)
	m.Rcode = dns.RcodeSuccess
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl}, A: net.ParseIP(ip).To4()}}
	return m
}

func TestParallelReturnFirstResponseUsesRaceAndCapsTTL(t *testing.T) {
	r := &Router{parallelReturn: true, config: &config.Config{ParallelReturn: config.ParallelReturnConfig{WarmCacheTTL: 5, AggregateCacheTTL: 30}}, aggregateCache: make(map[string]aggregateCacheEntry), aggregateWarmup: make(map[string]struct{})}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	clients := []client.DNSClient{
		&fakeParallelClient{resp: newAResp("example.com.", "1.1.1.1", 120), delay: 5 * time.Millisecond},
		&fakeParallelClient{resp: newAResp("example.com.", "8.8.8.8", 120), delay: 50 * time.Millisecond},
	}

	resp, err := r.resolveByMode(context.Background(), req, clients, "cn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected raced single answer, got %#v", resp)
	}
	if resp.Answer[0].Header().Ttl != 5 {
		t.Fatalf("expected raced response ttl to be capped to 5, got %d", resp.Answer[0].Header().Ttl)
	}
}

func TestParallelReturnSecondResponseUsesWarmAggregateCache(t *testing.T) {
	r := &Router{parallelReturn: true, config: &config.Config{ParallelReturn: config.ParallelReturnConfig{WarmCacheTTL: 5, AggregateCacheTTL: 30}}, aggregateCache: make(map[string]aggregateCacheEntry), aggregateWarmup: make(map[string]struct{})}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	clients := []client.DNSClient{
		&fakeParallelClient{resp: newAResp("example.com.", "1.1.1.1", 120), delay: 5 * time.Millisecond},
		&fakeParallelClient{resp: newAResp("example.com.", "8.8.8.8", 120), delay: 15 * time.Millisecond},
	}

	_, err := r.resolveByMode(context.Background(), req, clients, "cn")
	if err != nil {
		t.Fatalf("unexpected first call error: %v", err)
	}

	time.Sleep(40 * time.Millisecond)

	resp, err := r.resolveByMode(context.Background(), req, clients, "cn")
	if err != nil {
		t.Fatalf("unexpected second call error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected cached aggregated response")
	}
	if len(resp.Answer) != 2 {
		t.Fatalf("expected 2 answers from warm aggregate cache, got %d", len(resp.Answer))
	}
}

func TestAggregateCacheHitExtendsExpiry(t *testing.T) {
	r := &Router{parallelReturn: true, config: &config.Config{ParallelReturn: config.ParallelReturnConfig{WarmCacheTTL: 5, AggregateCacheTTL: 2}}, aggregateCache: make(map[string]aggregateCacheEntry), aggregateWarmup: make(map[string]struct{})}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	key := buildAggregateCacheKey(req, "cn")

	r.aggregateCache[key] = aggregateCacheEntry{
		resp:      newAResp("example.com.", "1.1.1.1", 120),
		expiresAt: time.Now().Add(500 * time.Millisecond),
	}

	before := r.aggregateCache[key].expiresAt
	r.touchAggregateCache(key, req.Copy(), nil)
	after := r.aggregateCache[key].expiresAt

	if !after.After(before) {
		t.Fatalf("expected cache expiry to extend, before=%v after=%v", before, after)
	}
}

func TestHotRefreshRespectsMinimumInterval(t *testing.T) {
	r := &Router{parallelReturn: true, config: &config.Config{ParallelReturn: config.ParallelReturnConfig{WarmCacheTTL: 5, AggregateCacheTTL: 30}}, aggregateCache: make(map[string]aggregateCacheEntry), aggregateWarmup: make(map[string]struct{})}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	key := buildAggregateCacheKey(req, "cn")

	r.aggregateCache[key] = aggregateCacheEntry{
		resp:           newAResp("example.com.", "1.1.1.1", 120),
		expiresAt:      time.Now().Add(30 * time.Second),
		hitCount:       hotAggregateRefreshThreshold,
		lastHotRefresh: time.Now(),
	}

	r.touchAggregateCache(key, req.Copy(), nil)

	if _, ok := r.aggregateWarmup[key]; ok {
		t.Fatal("expected hot refresh to be throttled by minimum interval")
	}
}
