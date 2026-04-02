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

	resp, err := AggregateResolve(context.Background(), req, clients)
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

	resp, err := AggregateResolve(context.Background(), req, clients)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("expected 1 deduped answer, got %d", len(resp.Answer))
	}
}
