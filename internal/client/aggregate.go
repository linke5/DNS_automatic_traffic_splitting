package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type aggregateResult struct {
	resp *dns.Msg
	err  error
	idx  int
}

func AggregateResolve(ctx context.Context, req *dns.Msg, clients []DNSClient) (*dns.Msg, error) {
	if len(clients) == 0 {
		return nil, fmt.Errorf("没有可用的上游客户端")
	}

	results := make(chan aggregateResult, len(clients))
	for i, c := range clients {
		reqClone := req.Copy()
		go func(idx int, cl DNSClient) {
			resp, err := cl.Resolve(ctx, reqClone)
			results <- aggregateResult{resp: resp, err: err, idx: idx}
		}(i, c)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	responses := make([]aggregateResult, 0, len(clients))
	var (
		bestFail         *dns.Msg
		bestFailPriority = failPriorityUnknown
		lastErr          error
	)

	for i := 0; i < len(clients); i++ {
		select {
		case r := <-results:
			if r.err != nil {
				lastErr = r.err
				continue
			}
			if r.resp == nil {
				continue
			}
			if r.resp.Rcode == dns.RcodeSuccess {
				responses = append(responses, r)
				continue
			}

			p := failPriority(r.resp.Rcode)
			if bestFail == nil || p < bestFailPriority {
				bestFail = r.resp
				bestFailPriority = p
			}
		case <-timer.C:
			if len(responses) > 0 {
				return mergeResponses(req, responses)
			}
			if bestFail != nil {
				return bestFail, nil
			}
			return nil, fmt.Errorf("并发聚合查询超时")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if len(responses) > 0 {
		return mergeResponses(req, responses)
	}
	if bestFail != nil {
		return bestFail, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("所有上游查询均失败: %w", lastErr)
	}
	return nil, fmt.Errorf("未知错误：未收到任何响应")
}

func mergeResponses(req *dns.Msg, results []aggregateResult) (*dns.Msg, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("没有可聚合的响应")
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].idx < results[j].idx
	})

	base := results[0].resp.Copy()
	seenAnswer := make(map[string]struct{})
	seenNs := make(map[string]struct{})
	seenExtra := make(map[string]struct{})

	base.Answer = dedupeRRs(base.Answer, seenAnswer)
	base.Ns = dedupeRRs(base.Ns, seenNs)
	base.Extra = dedupeRRs(base.Extra, seenExtra)

	for _, result := range results[1:] {
		base.Answer = append(base.Answer, dedupeRRs(result.resp.Answer, seenAnswer)...)
		base.Ns = append(base.Ns, dedupeRRs(result.resp.Ns, seenNs)...)
		base.Extra = append(base.Extra, dedupeRRs(result.resp.Extra, seenExtra)...)
	}

	base.SetReply(req)
	base.Rcode = dns.RcodeSuccess
	return base, nil
}

func dedupeRRs(records []dns.RR, seen map[string]struct{}) []dns.RR {
	merged := make([]dns.RR, 0, len(records))
	for _, rr := range records {
		if rr == nil {
			continue
		}
		key := strings.ToLower(rr.String())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, rr)
	}
	return merged
}
