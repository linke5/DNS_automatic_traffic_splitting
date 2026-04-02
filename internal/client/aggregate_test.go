package client

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
)

func newSuccessMsg(name string, qtype uint16, records ...dns.RR) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	m.Rcode = dns.RcodeSuccess
	m.Answer = append(m.Answer, records...)
	return m
}

func TestAggregateResolveMergesUniqueAnswers(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	clients := []DNSClient{
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("1.1.1.1").To4()})},
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("8.8.8.8").To4()})},
	}

	resp, err := AggregateResolve(context.Background(), req, clients, "median")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if len(resp.Answer) != 2 {
		t.Fatalf("expected 2 unique answers, got %d", len(resp.Answer))
	}
}

func TestAggregateResolveDedupesDuplicateAnswers(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	rr := &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("1.1.1.1").To4()}
	clients := []DNSClient{
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, rr)},
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, rr)},
	}

	resp, err := AggregateResolve(context.Background(), req, clients, "median")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected 1 deduped answer, got %d", len(resp.Answer))
	}
}

func TestAggregateResolveStripsUpstreamOPTAndECS(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	req.SetEdns0(1232, true)

	upstreamOPT := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
	upstreamOPT.SetUDPSize(4096)
	upstreamOPT.Option = append(upstreamOPT.Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, SourceScope: 24, Address: net.ParseIP("1.2.3.0").To4()})

	clients := []DNSClient{
		&fakeRaceClient{resp: &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess}, Question: req.Question, Answer: []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("1.1.1.1").To4()}}, Extra: []dns.RR{upstreamOPT}}},
	}

	resp, err := AggregateResolve(context.Background(), req, clients, "median")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if len(resp.Extra) != 1 {
		t.Fatalf("expected exactly one downstream OPT, got %d", len(resp.Extra))
	}
	opt, ok := resp.Extra[0].(*dns.OPT)
	if !ok {
		t.Fatalf("expected OPT in extra, got %T", resp.Extra[0])
	}
	if len(opt.Option) != 0 {
		t.Fatalf("expected upstream ECS to be stripped, got %d options", len(opt.Option))
	}
	if opt.UDPSize() != 1232 {
		t.Fatalf("expected downstream udp size 1232, got %d", opt.UDPSize())
	}
}

func TestAggregateResolveDedupesSameRecordWithDifferentTTLUsingMedianTTL(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	clients := []DNSClient{
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("1.1.1.1").To4()})},
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 12}, A: net.ParseIP("1.1.1.1").To4()})},
		&fakeRaceClient{resp: newSuccessMsg("example.com.", dns.TypeA, &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.ParseIP("1.1.1.1").To4()})},
	}

	resp, err := AggregateResolve(context.Background(), req, clients, "median")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected one deduped answer, got %d", len(resp.Answer))
	}
	if resp.Answer[0].Header().Ttl != 30 {
		t.Fatalf("expected median ttl 30, got %d", resp.Answer[0].Header().Ttl)
	}
}

func TestTrimToSingleRecordPerType(t *testing.T) {
	resp := new(dns.Msg)
	resp.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("1.1.1.1").To4()},
		&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("8.8.8.8").To4()},
		&dns.AAAA{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2400:3200::1")},
		&dns.AAAA{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2400:3200::2")},
	}

	TrimToSingleRecordPerType(resp)

	if len(resp.Answer) != 2 {
		t.Fatalf("expected 2 answers after trimming, got %d", len(resp.Answer))
	}
	if resp.Answer[0].Header().Rrtype != dns.TypeA {
		t.Fatalf("expected first answer type A, got %d", resp.Answer[0].Header().Rrtype)
	}
	if resp.Answer[1].Header().Rrtype != dns.TypeAAAA {
		t.Fatalf("expected second answer type AAAA, got %d", resp.Answer[1].Header().Rrtype)
	}
}
