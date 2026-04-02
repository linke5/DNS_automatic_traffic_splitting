package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"doh-autoproxy/internal/config"
	"doh-autoproxy/internal/router"
	"doh-autoproxy/internal/util"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type sharedDoHEntry struct {
	path   string
	router *router.Router
	mode   string
	addr   string
}

type SharedDoHServer struct {
	addr        string
	http2Server *http.Server
	http3Server *http3.Server
	entries     map[string]*sharedDoHEntry
	mu          sync.RWMutex
}

func NewSharedDoHServer(addr string, entries []sharedDoHEntry, tlsConfig *tls.Config) *SharedDoHServer {
	s := &SharedDoHServer{
		addr:    addr,
		entries: make(map[string]*sharedDoHEntry, len(entries)),
	}

	for i := range entries {
		entry := entries[i]
		s.entries[entry.path] = &entry
	}

	handler := http.HandlerFunc(s.serveHTTP)
	s.http2Server = &http.Server{
		Addr:         addr,
		Handler:      handler,
		TLSConfig:    tlsConfig,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	s.http3Server = &http3.Server{
		Addr:      addr,
		TLSConfig: tlsConfig,
		Handler:   handler,
		QUICConfig: &quic.Config{
			MaxIdleTimeout: 30 * time.Second,
		},
	}

	return s
}

func NewSharedDoHTLSConfig(cfg *config.Config, cm *util.CertManager) (*tls.Config, error) {
	if cm != nil && cm.GetCertificateFunc() != nil {
		return &tls.Config{
			GetCertificate: cm.GetCertificateFunc(),
			NextProtos:     []string{"h3", "h2", "http/1.1"},
		}, nil
	}

	var certs []tls.Certificate
	var err error
	if len(cfg.TLSCertificates) > 0 {
		certs, err = util.LoadServerCertificates(cfg.TLSCertificates)
	} else {
		certs, err = util.LoadServerCertificate("server.crt", "server.key")
	}
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: certs,
		NextProtos:   []string{"h3", "h2", "http/1.1"},
	}, nil
}

func (s *SharedDoHServer) Start() {
	go func() {
		log.Printf("Starting shared DoH (HTTP/1.1, HTTP/2) server on %s", s.addr)
		if err := s.http2Server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("共享 DoH HTTP/1.1, HTTP/2 服务器启动失败: %v", err)
		}
	}()

	go func() {
		log.Printf("Starting shared DoH (HTTP/3) server on %s", s.addr)
		udpPort := util.ParsePort(s.addr)
		udpAddr := &net.UDPAddr{Port: udpPort}
		udpConn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			log.Printf("共享 DoH HTTP/3 监听失败: %v", err)
			return
		}
		defer udpConn.Close()

		if err := s.http3Server.Serve(udpConn); err != nil && err != http.ErrServerClosed {
			log.Printf("共享 DoH HTTP/3 服务器启动失败: %v", err)
		}
	}()
}

func (s *SharedDoHServer) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.http2Server != nil {
		if err := s.http2Server.Shutdown(ctx); err != nil {
			return err
		}
	}
	if s.http3Server != nil {
		if err := s.http3Server.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *SharedDoHServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	entry := s.entries[r.URL.Path]
	s.mu.RUnlock()
	if entry == nil {
		http.NotFound(w, r)
		return
	}

	handler := &DoHRequestHandler{router: entry.router, path: entry.path}
	handler.listenAddr = entry.addr
	handler.mode = entry.mode
	handler.ServeHTTP(w, r)
}

func BuildSharedDoHEntries(mainCfg *config.Config, mainRouter *router.Router, parallelRouter *router.Router) ([]sharedDoHEntry, error) {
	entries := []sharedDoHEntry{}
	if mainCfg.Listen.DOH != "" {
		entries = append(entries, sharedDoHEntry{path: effectiveListenPath(mainCfg.Listen.DoHPath), router: mainRouter, mode: "standard", addr: mainCfg.Listen.DOH})
	}
	if mainCfg.ParallelReturn.Enabled && mainCfg.ParallelReturn.Listen.DOH != "" {
		entries = append(entries, sharedDoHEntry{path: effectiveListenPath(mainCfg.ParallelReturn.Listen.DoHPath), router: parallelRouter, mode: "parallel", addr: mainCfg.ParallelReturn.Listen.DOH})
	}

	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry.path]; ok {
			return nil, fmt.Errorf("duplicate DoH path registered on shared listener: %s", entry.path)
		}
		seen[entry.path] = struct{}{}
	}

	return entries, nil
}

func effectiveListenPath(path string) string {
	if path == "" {
		return "/dns-query"
	}
	if path[0] != '/' {
		return "/" + path
	}
	return path
}
