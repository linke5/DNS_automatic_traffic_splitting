package router

import (
	"context"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"doh-autoproxy/internal/client"
	"doh-autoproxy/internal/config"
	"doh-autoproxy/internal/querylog"
	"doh-autoproxy/internal/resolver"

	"github.com/miekg/dns"
)

type RegexRule struct {
	Pattern *regexp.Regexp
	Target  string
}

type Router struct {
	config          *config.Config
	geo             *GeoDataManager
	logger          *querylog.QueryLogger
	cnClients       []client.DNSClient
	overseasClients []client.DNSClient
	parallelReturn  bool

	cnStats       []*client.StatsClient
	overseasStats []*client.StatsClient

	regexRules []RegexRule

	aggregateCacheMu sync.RWMutex
	aggregateCache   map[string]aggregateCacheEntry
	aggregateWarmup  map[string]struct{}
}

type aggregateCacheEntry struct {
	resp           *dns.Msg
	expiresAt      time.Time
	hitCount       int
	lastHotRefresh time.Time
}

const hotAggregateRefreshThreshold = 3
const hotAggregateRefreshMinInterval = 15 * time.Second

func NewRouter(cfg *config.Config, geoManager *GeoDataManager, logger *querylog.QueryLogger) *Router {
	return NewRouterWithMode(cfg, geoManager, logger, false)
}

func NewRouterWithMode(cfg *config.Config, geoManager *GeoDataManager, logger *querylog.QueryLogger, parallelReturn bool) *Router {
	r := &Router{
		config:          cfg,
		geo:             geoManager,
		logger:          logger,
		parallelReturn:  parallelReturn,
		aggregateCache:  make(map[string]aggregateCacheEntry),
		aggregateWarmup: make(map[string]struct{}),
	}

	for domain, target := range cfg.Rules {
		if strings.HasPrefix(domain, "regexp:") {
			pattern := strings.TrimPrefix(domain, "regexp:")
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Printf("忽略无效的正则规则: %s -> %v", domain, err)
				continue
			}
			r.regexRules = append(r.regexRules, RegexRule{
				Pattern: re,
				Target:  target,
			})
		}
	}

	bootstrapper := resolver.NewBootstrapper(cfg.BootstrapDNS)

	for _, upstreamCfg := range cfg.Upstreams.CN {
		c, err := client.NewDNSClient(upstreamCfg, bootstrapper)
		if err != nil {
			log.Printf("Failed to initialize CN upstream %s: %v", upstreamCfg.Address, err)
			continue
		}
		groupName := "CN"
		if parallelReturn {
			groupName = "Parallel-CN"
		}
		sc := client.NewStatsClient(c, upstreamCfg.Address, upstreamCfg.Protocol, groupName)
		r.cnClients = append(r.cnClients, sc)
		r.cnStats = append(r.cnStats, sc)
	}

	for _, upstreamCfg := range cfg.Upstreams.Overseas {
		c, err := client.NewDNSClient(upstreamCfg, bootstrapper)
		if err != nil {
			log.Printf("Failed to initialize Overseas upstream %s: %v", upstreamCfg.Address, err)
			continue
		}
		groupName := "Overseas"
		if parallelReturn {
			groupName = "Parallel-Overseas"
		}
		sc := client.NewStatsClient(c, upstreamCfg.Address, upstreamCfg.Protocol, groupName)
		r.overseasClients = append(r.overseasClients, sc)
		r.overseasStats = append(r.overseasStats, sc)
	}

	return r
}

func (r *Router) GetUpstreamStats() []interface{} {
	var stats []interface{}
	for _, s := range r.cnStats {
		stats = append(stats, s.GetStats())
	}
	for _, s := range r.overseasStats {
		stats = append(stats, s.GetStats())
	}
	return stats
}

func (r *Router) Route(ctx context.Context, req *dns.Msg, clientIP string) (*dns.Msg, error) {
	start := time.Now()
	if len(req.Question) == 0 {
		return nil, fmt.Errorf("no question")
	}

	downstreamECS := client.ExtractECS(req)
	resp, upstream, err := r.routeInternal(ctx, req)
	meta := RequestMetaFromContext(ctx)

	duration := time.Since(start).Milliseconds()

	qName := req.Question[0].Name
	qType := dns.Type(req.Question[0].Qtype).String()

	status := "ERROR"
	answer := ""
	var answerRecords []querylog.AnswerRecord

	if err == nil && resp != nil {
		status = dns.RcodeToString[resp.Rcode]
		if len(resp.Answer) > 0 {
			parts := strings.Fields(resp.Answer[0].String())
			if len(parts) > 4 {
				answer = strings.Join(parts[4:], " ")
			} else {
				answer = resp.Answer[0].String()
			}
			if len(resp.Answer) > 1 {
				answer += fmt.Sprintf(" (+%d more)", len(resp.Answer)-1)
			}

			for _, ans := range resp.Answer {
				parts := strings.Fields(ans.String())
				data := ""
				if len(parts) > 4 {
					data = strings.Join(parts[4:], " ")
				} else {
					data = ans.String()
				}
				answerRecords = append(answerRecords, querylog.AnswerRecord{
					Name: ans.Header().Name,
					Type: dns.Type(ans.Header().Rrtype).String(),
					TTL:  ans.Header().Ttl,
					Data: data,
				})
			}
		}
	}

	if r.logger != nil {
		r.logger.AddLog(&querylog.LogEntry{
			ClientIP:      clientIP,
			Listener:      meta.Listener,
			ListenerPort:  meta.ListenerPort,
			ServiceMode:   meta.ServiceMode,
			ReturnMode:    meta.ReturnMode,
			DownstreamECS: downstreamECS,
			Domain:        qName,
			Type:          qType,
			Upstream:      upstream,
			Answer:        answer,
			AnswerRecords: answerRecords,
			DurationMs:    duration,
			Status:        status,
		})
	}

	if resp != nil && resp.Rcode == dns.RcodeNameError {
		for _, ans := range resp.Answer {
			ans.Header().Ttl = 0
		}
		for _, ns := range resp.Ns {
			ns.Header().Ttl = 0
		}
		for _, extra := range resp.Extra {
			extra.Header().Ttl = 0
		}
	}

	return resp, err
}

func (r *Router) routeInternal(ctx context.Context, req *dns.Msg) (*dns.Msg, string, error) {
	qName := strings.ToLower(strings.TrimSuffix(req.Question[0].Name, "."))

	if ipStr, ok := r.config.Hosts[qName]; ok {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return nil, "Hosts", fmt.Errorf("自定义Hosts中存在无效IP地址: %s for %s", ipStr, qName)
		}

		m := new(dns.Msg)
		m.SetReply(req)
		rrHeader := dns.RR_Header{
			Name:   req.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			m.Answer = append(m.Answer, &dns.A{Hdr: rrHeader, A: ipv4})
		} else {
			rrHeader.Rrtype = dns.TypeAAAA
			m.Answer = append(m.Answer, &dns.AAAA{Hdr: rrHeader, AAAA: ip})
		}
		return m, "Hosts", nil
	}

	if rule, ok := r.config.Rules[qName]; ok {
		switch strings.ToLower(rule) {
		case "cn":
			resp, err := r.resolveByMode(ctx, req, r.cnClients, "cn")
			return resp, "Rule(CN)", err
		case "overseas":
			resp, err := r.resolveByMode(ctx, req, r.overseasClients, "overseas")
			return resp, "Rule(Overseas)", err
		default:
		}
	}

	for _, rr := range r.regexRules {
		if rr.Pattern.MatchString(qName) {
			switch strings.ToLower(rr.Target) {
			case "cn":
				resp, err := r.resolveByMode(ctx, req, r.cnClients, "cn")
				return resp, "Rule(Regex/CN)", err
			case "overseas":
				resp, err := r.resolveByMode(ctx, req, r.overseasClients, "overseas")
				return resp, "Rule(Regex/Overseas)", err
			}
		}
	}

	if geoSiteRule := r.geo.LookupGeoSite(qName); geoSiteRule != "" {
		switch strings.ToLower(geoSiteRule) {
		case "cn":
			resp, err := r.resolveByMode(ctx, req, r.cnClients, "cn")
			return resp, "GeoSite(CN)", err
		default:
			resp, err := r.resolveByMode(ctx, req, r.overseasClients, "overseas")
			return resp, "GeoSite(Overseas)", err
		}
	}

	// GeoSite 未命中：同时查询国内和海外 DNS，根据结果判断
	type dualResult struct {
		resp   *dns.Msg
		err    error
		source string
	}

	dualCh := make(chan dualResult, 2)

	go func() {
		resp, err := r.resolveByMode(ctx, req.Copy(), r.overseasClients, "overseas")
		dualCh <- dualResult{resp: resp, err: err, source: "overseas"}
	}()
	go func() {
		resp, err := r.resolveByMode(ctx, req.Copy(), r.cnClients, "cn")
		dualCh <- dualResult{resp: resp, err: err, source: "cn"}
	}()

	var overseasResult, cnResult *dualResult
	for i := 0; i < 2; i++ {
		r := <-dualCh
		rCopy := r
		if r.source == "overseas" {
			overseasResult = &rCopy
		} else {
			cnResult = &rCopy
		}
	}

	// 海外成功且有应答
	if overseasResult.err == nil && overseasResult.resp != nil && overseasResult.resp.Rcode == dns.RcodeSuccess {
		var resolvedIP net.IP
		for _, ans := range overseasResult.resp.Answer {
			if a, ok := ans.(*dns.A); ok {
				resolvedIP = a.A
				break
			}
			if aaaa, ok := ans.(*dns.AAAA); ok {
				resolvedIP = aaaa.AAAA
				break
			}
		}

		// 如果解析出的 IP 是国内的，用国内 DNS 的结果（更准确）
		if resolvedIP != nil && r.geo.IsCNIP(resolvedIP) {
			if cnResult.err == nil && cnResult.resp != nil && cnResult.resp.Rcode == dns.RcodeSuccess {
				return cnResult.resp, "GeoIP(CN)", nil
			}
			// 国内 DNS 失败，仍然返回海外结果
			return overseasResult.resp, "GeoIP(CN/Fallback-Overseas)", nil
		}

		return overseasResult.resp, "GeoIP(Overseas)", nil
	}

	// 海外失败或 NXDOMAIN/SERVFAIL，尝试用国内结果
	if cnResult.err == nil && cnResult.resp != nil && cnResult.resp.Rcode == dns.RcodeSuccess {
		log.Printf("海外DNS解析失败或无结果，使用国内DNS结果: %s", qName)
		return cnResult.resp, "GeoIP(Fallback/CN)", nil
	}

	// 两边都失败，返回最有意义的错误
	if overseasResult.err == nil && overseasResult.resp != nil {
		return overseasResult.resp, "GeoIP(Overseas)", nil
	}
	if cnResult.err == nil && cnResult.resp != nil {
		return cnResult.resp, "GeoIP(CN)", nil
	}

	// 全部出错
	if overseasResult.err != nil {
		return nil, "GeoIP(Error)", fmt.Errorf("所有DNS查询均失败: overseas=%v, cn=%v", overseasResult.err, cnResult.err)
	}
	return nil, "GeoIP(Error)", cnResult.err
}

func (r *Router) resolveByMode(ctx context.Context, req *dns.Msg, clients []client.DNSClient, group string) (*dns.Msg, error) {
	if !r.parallelReturn {
		return client.RaceResolve(ctx, req, clients)
	}

	cacheKey := buildAggregateCacheKey(req, group)
	if cached := r.getAggregateCache(cacheKey); cached != nil {
		setRequestReturnMode(ctx, "aggregate-cache")
		r.touchAggregateCache(cacheKey, req.Copy(), clients)
		return cached, nil
	}

	resp, err := client.RaceResolve(ctx, req, clients)
	if err == nil && resp != nil {
		capResponseTTL(resp, uint32(r.parallelWarmCacheTTLSeconds()))
		if r.parallelSingleRecordPerType() {
			client.TrimToSingleRecordPerType(resp)
		}
	}
	setRequestReturnMode(ctx, "race-first")
	r.scheduleAggregateWarmup(cacheKey, req.Copy(), clients)
	return resp, err
}

func buildAggregateCacheKey(req *dns.Msg, group string) string {
	if req == nil || len(req.Question) == 0 {
		return group
	}
	q := req.Question[0]
	ecs := client.ExtractECS(req)
	return fmt.Sprintf("%s|%s|%d|%d|%s", group, strings.ToLower(q.Name), q.Qtype, q.Qclass, ecs)
}

func (r *Router) getAggregateCache(key string) *dns.Msg {
	r.aggregateCacheMu.RLock()
	entry, ok := r.aggregateCache[key]
	r.aggregateCacheMu.RUnlock()
	if !ok {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		r.aggregateCacheMu.Lock()
		delete(r.aggregateCache, key)
		r.aggregateCacheMu.Unlock()
		return nil
	}
	if entry.resp == nil {
		return nil
	}
	return entry.resp.Copy()
}

func (r *Router) touchAggregateCache(key string, req *dns.Msg, clients []client.DNSClient) {
	r.aggregateCacheMu.Lock()
	entry, ok := r.aggregateCache[key]
	if !ok || entry.resp == nil {
		r.aggregateCacheMu.Unlock()
		return
	}
	cacheTTL := r.parallelAggregateCacheTTLForResponse(entry.resp)
	entry.hitCount++
	entry.expiresAt = time.Now().Add(cacheTTL)
	canRefresh := entry.hitCount >= hotAggregateRefreshThreshold && time.Since(entry.lastHotRefresh) >= hotAggregateRefreshMinInterval
	if canRefresh {
		entry.lastHotRefresh = time.Now()
	}
	r.aggregateCache[key] = entry
	r.aggregateCacheMu.Unlock()

	if canRefresh {
		if r.logger != nil {
			r.logger.RecordHotRefreshTrigger()
		}
		r.scheduleAggregateWarmup(key, req, clients)
	}
}

func (r *Router) scheduleAggregateWarmup(key string, req *dns.Msg, clients []client.DNSClient) {
	r.aggregateCacheMu.Lock()
	if _, ok := r.aggregateWarmup[key]; ok {
		r.aggregateCacheMu.Unlock()
		return
	}
	r.aggregateWarmup[key] = struct{}{}
	r.aggregateCacheMu.Unlock()

	go func() {
		defer func() {
			r.aggregateCacheMu.Lock()
			delete(r.aggregateWarmup, key)
			r.aggregateCacheMu.Unlock()
		}()

		cacheTTL := r.parallelAggregateCacheTTLForResponse(nil)
		warmCtx, cancel := context.WithTimeout(context.Background(), cacheTTL)
		defer cancel()

		resp, err := client.AggregateResolve(warmCtx, req, clients, r.parallelAggregateTTLStrategy())
		if err != nil || resp == nil || resp.Rcode != dns.RcodeSuccess || len(resp.Answer) == 0 {
			if r.logger != nil {
				r.logger.RecordAggregateWarmup(false)
			}
			return
		}

		cacheTTL = r.parallelAggregateCacheTTLForResponse(resp)

		r.aggregateCacheMu.Lock()
		r.aggregateCache[key] = aggregateCacheEntry{
			resp:           resp.Copy(),
			expiresAt:      time.Now().Add(cacheTTL),
			hitCount:       0,
			lastHotRefresh: time.Time{},
		}
		r.aggregateCacheMu.Unlock()
		if r.logger != nil {
			r.logger.RecordAggregateWarmup(true)
		}
	}()
}

func (r *Router) parallelWarmCacheTTL() time.Duration {
	return time.Duration(r.parallelWarmCacheTTLSeconds()) * time.Second
}

func (r *Router) parallelAggregateCacheTTLForResponse(resp *dns.Msg) time.Duration {
	if r.parallelAggregateCacheTTLMode() == "upstream" {
		if ttl := minResponseTTL(resp); ttl > 0 {
			return time.Duration(ttl) * time.Second
		}
	}
	return time.Duration(r.parallelAggregateCacheTTLSeconds()) * time.Second
}

func (r *Router) parallelWarmCacheTTLSeconds() int {
	if r.config != nil && r.config.ParallelReturn.WarmCacheTTL > 0 {
		return r.config.ParallelReturn.WarmCacheTTL
	}
	return 5
}

func (r *Router) parallelAggregateCacheTTLSeconds() int {
	if r.config != nil && r.config.ParallelReturn.AggregateCacheTTL > 0 {
		return r.config.ParallelReturn.AggregateCacheTTL
	}
	return 30
}

func (r *Router) parallelAggregateCacheTTLMode() string {
	if r.config != nil && r.config.ParallelReturn.AggregateCacheTTLMode != "" {
		return r.config.ParallelReturn.AggregateCacheTTLMode
	}
	return "fixed"
}

func (r *Router) parallelAggregateTTLStrategy() string {
	if r.config != nil && r.config.ParallelReturn.AggregateTTLStrategy != "" {
		return r.config.ParallelReturn.AggregateTTLStrategy
	}
	return "median"
}

func (r *Router) parallelSingleRecordPerType() bool {
	if r.config != nil {
		return r.config.ParallelReturn.SingleRecordPerType
	}
	return false
}

func minResponseTTL(resp *dns.Msg) uint32 {
	if resp == nil {
		return 0
	}
	var minTTL uint32
	update := func(records []dns.RR) {
		for _, rr := range records {
			if rr == nil {
				continue
			}
			ttl := rr.Header().Ttl
			if ttl == 0 {
				continue
			}
			if minTTL == 0 || ttl < minTTL {
				minTTL = ttl
			}
		}
	}
	update(resp.Answer)
	update(resp.Ns)
	update(resp.Extra)
	return minTTL
}

func setRequestReturnMode(ctx context.Context, mode string) {
	if ctx == nil {
		return
	}
	meta := RequestMetaFromContext(ctx)
	meta.ReturnMode = mode
	if holder, ok := ctx.Value(requestMetaHolderKey{}).(*RequestMeta); ok && holder != nil {
		*holder = meta
	}
}

func capResponseTTL(resp *dns.Msg, ttl uint32) {
	if resp == nil {
		return
	}
	for _, rr := range resp.Answer {
		if rr != nil && rr.Header().Ttl > ttl {
			rr.Header().Ttl = ttl
		}
	}
	for _, rr := range resp.Ns {
		if rr != nil && rr.Header().Ttl > ttl {
			rr.Header().Ttl = ttl
		}
	}
	for _, rr := range resp.Extra {
		if rr != nil && rr.Header().Ttl > ttl {
			rr.Header().Ttl = ttl
		}
	}
}
