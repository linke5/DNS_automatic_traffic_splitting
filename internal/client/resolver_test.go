package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type fakeRaceClient struct {
	resp  *dns.Msg
	err   error
	delay time.Duration
}

func (f *fakeRaceClient) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.resp, f.err
}

func newFailMsg(rcode int) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(&dns.Msg{}, rcode)
	return m
}

func TestRaceResolvePrefersNXDOMAINOverSERVFAIL(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("_8084._https.example.com.", dns.TypeHTTPS)

	clients := []DNSClient{
		&fakeRaceClient{resp: newFailMsg(dns.RcodeServerFailure), delay: 5 * time.Millisecond},
		&fakeRaceClient{resp: newFailMsg(dns.RcodeNameError), delay: 15 * time.Millisecond},
	}

	resp, err := RaceResolve(context.Background(), req, clients)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got %s", dns.RcodeToString[resp.Rcode])
	}
}

func TestRaceResolvePrefersNXDOMAINOverNOTIMP(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("_8443._https.example.com.", dns.TypeHTTPS)

	clients := []DNSClient{
		&fakeRaceClient{resp: newFailMsg(dns.RcodeNotImplemented), delay: 3 * time.Millisecond},
		&fakeRaceClient{resp: newFailMsg(dns.RcodeNameError), delay: 10 * time.Millisecond},
	}

	resp, err := RaceResolve(context.Background(), req, clients)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got %s", dns.RcodeToString[resp.Rcode])
	}
}

func TestRaceResolveReturnsErrorWhenAllClientsFail(t *testing.T) {
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	clients := []DNSClient{
		&fakeRaceClient{err: errors.New("upstream one failed")},
		&fakeRaceClient{err: errors.New("upstream two failed")},
	}

	resp, err := RaceResolve(context.Background(), req, clients)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if resp != nil {
		t.Fatal("expected nil response when all clients fail")
	}
}
