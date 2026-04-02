package server

import (
	"log"
	"time"

	"doh-autoproxy/internal/config"
	"doh-autoproxy/internal/router"
	"doh-autoproxy/internal/util"

	"github.com/miekg/dns"
)

type ParallelReturnServer struct {
	udpServer *dns.Server
	tcpServer *dns.Server
	dotServer *DoTServer
	doqServer *DoQServer
	dohServer *DoHServer
	router    *router.Router
}

func NewParallelReturnServer(listen config.ListenConfig, tlsCerts []config.TLSCertConfig, autoCert config.AutoCertConfig, r *router.Router, cm *util.CertManager) *ParallelReturnServer {
	handlerUDP := &DNSRequestHandler{router: r, protocol: "udp", listenAddr: listen.DNSUDP, mode: "parallel"}
	handlerTCP := &DNSRequestHandler{router: r, protocol: "tcp", listenAddr: listen.DNSTCP, mode: "parallel"}

	var udpServer, tcpServer *dns.Server
	if listen.DNSUDP != "" {
		udpServer = &dns.Server{Addr: listen.DNSUDP, Net: "udp", Handler: handlerUDP, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	}
	if listen.DNSTCP != "" {
		tcpServer = &dns.Server{Addr: listen.DNSTCP, Net: "tcp", Handler: handlerTCP, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	}

	parallelCfg := &config.Config{
		Listen:          listen,
		TLSCertificates: tlsCerts,
		AutoCert:        autoCert,
	}

	var dohServer *DoHServer
	if listen.DOH != "" {
		dohServer = NewDoHServerWithMode(parallelCfg, r, cm, "parallel")
	}

	var dotServer *DoTServer
	if listen.DOT != "" {
		dotServer = NewDoTServerWithMode(parallelCfg, r, cm, "parallel")
	}

	var doqServer *DoQServer
	if listen.DOQ != "" {
		doqServer = NewDoQServerWithMode(parallelCfg, r, cm, "parallel")
	}

	return &ParallelReturnServer{
		udpServer: udpServer,
		tcpServer: tcpServer,
		dotServer: dotServer,
		doqServer: doqServer,
		dohServer: dohServer,
		router:    r,
	}
}

func (s *ParallelReturnServer) Start() {
	s.StartPartial(true, true, true)
}

func (s *ParallelReturnServer) StartPartial(startDoH, startDoT, startDoQ bool) {
	if s.udpServer != nil {
		go func() {
			log.Printf("Starting parallel-return UDP DNS server on %s", s.udpServer.Addr)
			if err := s.udpServer.ListenAndServe(); err != nil {
				log.Printf("无法启动 parallel-return UDP DNS服务器: %v", err)
			}
		}()
	}

	if s.tcpServer != nil {
		go func() {
			log.Printf("Starting parallel-return TCP DNS server on %s", s.tcpServer.Addr)
			if err := s.tcpServer.ListenAndServe(); err != nil {
				log.Printf("无法启动 parallel-return TCP DNS服务器: %v", err)
			}
		}()
	}

	if startDoT && s.dotServer != nil {
		s.dotServer.Start()
	}

	if startDoQ && s.doqServer != nil {
		s.doqServer.Start()
	}

	if startDoH && s.dohServer != nil {
		s.dohServer.Start()
	}
}

func (s *ParallelReturnServer) Stop() error {
	if s.udpServer != nil {
		if err := s.udpServer.Shutdown(); err != nil {
			return err
		}
	}
	if s.tcpServer != nil {
		if err := s.tcpServer.Shutdown(); err != nil {
			return err
		}
	}
	if s.dotServer != nil {
		if err := s.dotServer.Stop(); err != nil {
			return err
		}
	}
	if s.doqServer != nil {
		if err := s.doqServer.Stop(); err != nil {
			return err
		}
	}
	if s.dohServer != nil {
		if err := s.dohServer.Stop(); err != nil {
			return err
		}
	}
	return nil
}
