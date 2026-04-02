package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"doh-autoproxy/internal/config"
	"doh-autoproxy/internal/router"
	"doh-autoproxy/internal/util"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
)

type SharedDoTServer struct {
	addr        string
	listener    net.Listener
	mainSNI     string
	main        *router.Router
	parallelSNI string
	parallel    *router.Router
	tlsConfig   *tls.Config
	stopOnce    sync.Once
}

func NewSharedDoTServer(addr, mainSNI string, mainRouter *router.Router, parallelSNI string, parallelRouter *router.Router, tlsConfig *tls.Config) *SharedDoTServer {
	return &SharedDoTServer{addr: addr, mainSNI: strings.ToLower(strings.TrimSpace(mainSNI)), main: mainRouter, parallelSNI: strings.ToLower(strings.TrimSpace(parallelSNI)), parallel: parallelRouter, tlsConfig: tlsConfig}
}

func (s *SharedDoTServer) Start() {
	go func() {
		ln, err := tls.Listen("tcp", s.addr, s.tlsConfig)
		if err != nil {
			log.Printf("无法启动共享 DoT 服务器: %v", err)
			return
		}
		s.listener = ln
		log.Printf("Starting shared DoT server on %s", s.addr)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleConn(conn)
		}
	}()
}

func (s *SharedDoTServer) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *SharedDoTServer) handleConn(conn net.Conn) {
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return
	}
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	sni := strings.ToLower(strings.TrimSpace(tlsConn.ConnectionState().ServerName))
	r, mode := s.selectRouter(sni)
	if r == nil {
		return
	}

	reader := bufio.NewReader(tlsConn)
	for {
		_ = tlsConn.SetReadDeadline(time.Now().Add(10 * time.Second))
		lengthBytes := make([]byte, 2)
		if _, err := reader.Read(lengthBytes); err != nil {
			return
		}
		msgLen := int(lengthBytes[0])<<8 | int(lengthBytes[1])
		msg := make([]byte, msgLen)
		if _, err := reader.Read(msg); err != nil {
			return
		}

		req := new(dns.Msg)
		if err := req.Unpack(msg); err != nil {
			return
		}
		clientIP, _, _ := net.SplitHostPort(tlsConn.RemoteAddr().String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ctx = router.WithRequestMeta(ctx, router.RequestMeta{
			Listener:     "dot",
			ListenerPort: s.addr,
			ServiceMode:  mode,
		})
		resp, err := r.Route(ctx, req, clientIP)
		cancel()
		if err != nil {
			resp = new(dns.Msg)
			resp.SetRcode(req, dns.RcodeServerFailure)
		}
		packed, err := resp.Pack()
		if err != nil {
			return
		}
		out := append([]byte{byte(len(packed) >> 8), byte(len(packed))}, packed...)
		if _, err := tlsConn.Write(out); err != nil {
			return
		}
	}
}

func (s *SharedDoTServer) selectRouter(sni string) (*router.Router, string) {
	if sni == s.mainSNI {
		return s.main, "standard"
	}
	if sni == s.parallelSNI {
		return s.parallel, "parallel"
	}
	return nil, ""
}

type SharedDoQServer struct {
	addr        string
	listener    *quic.Listener
	mainSNI     string
	main        *router.Router
	parallelSNI string
	parallel    *router.Router
	tlsConfig   *tls.Config
}

func NewSharedDoQServer(addr, mainSNI string, mainRouter *router.Router, parallelSNI string, parallelRouter *router.Router, tlsConfig *tls.Config) *SharedDoQServer {
	return &SharedDoQServer{addr: addr, mainSNI: strings.ToLower(strings.TrimSpace(mainSNI)), main: mainRouter, parallelSNI: strings.ToLower(strings.TrimSpace(parallelSNI)), parallel: parallelRouter, tlsConfig: tlsConfig}
}

func (s *SharedDoQServer) Start() {
	go func() {
		listener, err := quic.ListenAddr(s.addr, s.tlsConfig, &quic.Config{MaxIdleTimeout: 30 * time.Second})
		if err != nil {
			log.Printf("无法启动共享 DoQ 服务器: %v", err)
			return
		}
		s.listener = listener
		log.Printf("Starting shared DoQ server on %s", s.addr)
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			go s.handleConn(conn)
		}
	}()
}

func (s *SharedDoQServer) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *SharedDoQServer) handleConn(conn *quic.Conn) {
	defer conn.CloseWithError(quic.ApplicationErrorCode(quic.NoError), "Connection closed")
	sni := strings.ToLower(strings.TrimSpace(conn.ConnectionState().TLS.ServerName))
	r, mode := s.selectRouter(sni)
	if r == nil {
		return
	}
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go handleDoQStreamWithRouter(stream, conn.RemoteAddr(), r, s.addr, mode)
	}
}

func (s *SharedDoQServer) selectRouter(sni string) (*router.Router, string) {
	if sni == s.mainSNI {
		return s.main, "standard"
	}
	if sni == s.parallelSNI {
		return s.parallel, "parallel"
	}
	return nil, ""
}

func NewSharedTLSServerConfig(cfg *config.Config, cm *util.CertManager, nextProto string) (*tls.Config, error) {
	if cm != nil && cm.GetCertificateFunc() != nil {
		return &tls.Config{GetCertificate: cm.GetCertificateFunc(), NextProtos: []string{nextProto}}, nil
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
	return &tls.Config{Certificates: certs, NextProtos: []string{nextProto}}, nil
}

func ValidateSharedTLSServerInputs(mainPort, mainSNI, parallelPort, parallelSNI, protocol string) error {
	if mainPort == "" || parallelPort == "" || mainPort != parallelPort {
		return nil
	}
	if strings.TrimSpace(mainSNI) == "" || strings.TrimSpace(parallelSNI) == "" {
		return fmt.Errorf("%s 共享端口时必须配置不同的 SNI", protocol)
	}
	if strings.EqualFold(strings.TrimSpace(mainSNI), strings.TrimSpace(parallelSNI)) {
		return fmt.Errorf("%s 共享端口时必须配置不同的 SNI", protocol)
	}
	return nil
}
