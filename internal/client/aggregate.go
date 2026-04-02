package client

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type aggregateResult struct {
	resp *dns.Msg
	err  error
	idx  int
}

func AggregateResolve(ctx context.Context, req *dns.Msg, clients []DNSClient, ttlStrategy string) (*dns.Msg, error) {
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
				return mergeResponses(req, responses, ttlStrategy)
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
		return mergeResponses(req, responses, ttlStrategy)
	}
	if bestFail != nil {
		return bestFail, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("所有上游查询均失败: %w", lastErr)
	}
	return nil, fmt.Errorf("未知错误：未收到任何响应")
}

func mergeResponses(req *dns.Msg, results []aggregateResult, ttlStrategy string) (*dns.Msg, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("没有可聚合的响应")
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].idx < results[j].idx
	})

	base := results[0].resp.Copy()
	seenAnswer := make(map[string]*rrAggregate)
	seenNs := make(map[string]*rrAggregate)
	seenExtra := make(map[string]*rrAggregate)

	base.Extra = sanitizeExtraForDownstream(req, base.Extra)
	base.Answer = dedupeRRs(base.Answer, seenAnswer, ttlStrategy)
	base.Ns = dedupeRRs(base.Ns, seenNs, ttlStrategy)
	base.Extra = dedupeRRs(base.Extra, seenExtra, ttlStrategy)

	for _, result := range results[1:] {
		base.Answer = append(base.Answer, dedupeRRs(result.resp.Answer, seenAnswer, ttlStrategy)...)
		base.Ns = append(base.Ns, dedupeRRs(result.resp.Ns, seenNs, ttlStrategy)...)
		base.Extra = append(base.Extra, dedupeRRs(sanitizeExtraForDownstream(req, result.resp.Extra), seenExtra, ttlStrategy)...)
	}

	base.SetReply(req)
	base.Rcode = dns.RcodeSuccess
	return base, nil
}

type rrAggregate struct {
	rr   dns.RR
	ttls []uint32
}

func dedupeRRs(records []dns.RR, seen map[string]*rrAggregate, ttlStrategy string) []dns.RR {
	merged := make([]dns.RR, 0, len(records))
	for _, rr := range records {
		if rr == nil {
			continue
		}
		key := rrIdentityKey(rr)
		if existing, ok := seen[key]; ok {
			existing.ttls = append(existing.ttls, rr.Header().Ttl)
			existing.rr.Header().Ttl = aggregateTTL(existing.ttls, ttlStrategy)
			continue
		}
		seen[key] = &rrAggregate{rr: rr, ttls: []uint32{rr.Header().Ttl}}
		merged = append(merged, rr)
	}
	return merged
}

func rrIdentityKey(rr dns.RR) string {
	if rr == nil {
		return ""
	}
	hdr := rr.Header()
	return strings.ToLower(strings.Join([]string{
		hdr.Name,
		strconv.FormatUint(uint64(hdr.Rrtype), 10),
		strconv.FormatUint(uint64(hdr.Class), 10),
		rrDataKey(rr),
	}, "|"))
}

func rrDataKey(rr dns.RR) string {
	if rr == nil {
		return ""
	}
	hdr := rr.Header()
	ttl := hdr.Ttl
	hdr.Ttl = 0
	defer func() { hdr.Ttl = ttl }()
	parts := strings.Fields(rr.String())
	if len(parts) > 4 {
		return strings.Join(parts[4:], " ")
	}
	return rr.String()
}

func medianTTL(ttls []uint32) uint32 {
	if len(ttls) == 0 {
		return 0
	}
	sorted := append([]uint32(nil), ttls...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return uint32((uint64(sorted[mid-1]) + uint64(sorted[mid])) / 2)
}

func aggregateTTL(ttls []uint32, ttlStrategy string) uint32 {
	switch ttlStrategy {
	case "min":
		return minTTLValue(ttls)
	case "max":
		return maxTTLValue(ttls)
	case "avg":
		return avgTTLValue(ttls)
	default:
		return medianTTL(ttls)
	}
}

func minTTLValue(ttls []uint32) uint32 {
	if len(ttls) == 0 {
		return 0
	}
	min := ttls[0]
	for _, ttl := range ttls[1:] {
		if ttl < min {
			min = ttl
		}
	}
	return min
}

func maxTTLValue(ttls []uint32) uint32 {
	if len(ttls) == 0 {
		return 0
	}
	max := ttls[0]
	for _, ttl := range ttls[1:] {
		if ttl > max {
			max = ttl
		}
	}
	return max
}

func avgTTLValue(ttls []uint32) uint32 {
	if len(ttls) == 0 {
		return 0
	}
	var sum uint64
	for _, ttl := range ttls {
		sum += uint64(ttl)
	}
	return uint32(sum / uint64(len(ttls)))
}

func sanitizeExtraForDownstream(req *dns.Msg, records []dns.RR) []dns.RR {
	filtered := make([]dns.RR, 0, len(records))
	for _, rr := range records {
		if rr == nil {
			continue
		}
		if rr.Header().Rrtype == dns.TypeOPT {
			continue
		}
		filtered = append(filtered, rr)
	}

	if req == nil {
		return filtered
	}
	if opt := req.IsEdns0(); opt != nil {
		cleanOpt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
		cleanOpt.SetUDPSize(opt.UDPSize())
		cleanOpt.SetVersion(opt.Version())
		if opt.Do() {
			cleanOpt.SetDo()
		}
		filtered = append(filtered, cleanOpt)
	}

	return filtered
}

func TrimToSingleRecordPerType(resp *dns.Msg) {
	if resp == nil {
		return
	}
	seen := make(map[string]struct{})
	trimmed := make([]dns.RR, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		if rr == nil {
			continue
		}
		key := rr.Header().Name + "|" + dns.Type(rr.Header().Rrtype).String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		trimmed = append(trimmed, rr)
	}
	resp.Answer = trimmed
}
