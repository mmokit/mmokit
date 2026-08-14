package universe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// resolveTLSConfig decides the TLS posture for the client-facing HTTP
// listeners. Precedence:
//  1. Explicit cert/key files (both non-empty) -> load them.
//  2. mode == "self-signed" -> generate an in-memory dev cert.
//  3. otherwise -> nil (serve plaintext).
//
// The bool return reports whether the returned config uses a self-signed dev
// cert, so the caller can log the "not for production" banner.
func resolveTLSConfig(certFile, keyFile, mode string) (*tls.Config, bool, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, false, fmt.Errorf("load TLS keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, false, nil
	}
	if mode == "self-signed" {
		cert, err := generateDevCert()
		if err != nil {
			return nil, false, fmt.Errorf("generate dev cert: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, true, nil
	}
	return nil, false, nil
}

// generateDevCert builds an in-memory self-signed certificate valid for
// localhost / 127.0.0.1 / ::1. Never written to disk. Dev/testing only.
func generateDevCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mmokit-dev"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// isLoopbackBind reports whether addr binds only the loopback interface.
// An empty host (":8080") binds all interfaces, so it is NOT loopback-only.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// httpTLSConfig resolves the shared TLS config once and memoizes it, so both
// HTTP listeners present the same certificate. A configuration error is logged
// and falls back to plaintext (which then triggers the non-loopback warning).
func (c *Process) httpTLSConfig() (*tls.Config, bool) {
	c.tlsOnce.Do(func() {
		cfg, selfSigned, err := resolveTLSConfig(c.cfg.TLSCertFile, c.cfg.TLSKeyFile, c.cfg.TLSMode)
		if err != nil {
			// An explicit cert/key request that fails to load is a
			// misconfiguration the operator must notice — log it loudly rather
			// than silently downgrading an intended-TLS listener to plaintext.
			if c.cfg.TLSCertFile != "" || c.cfg.TLSKeyFile != "" {
				c.Log.Log(CatMeshCell, "tls: WARNING --tls-cert/--tls-key were set but the keypair FAILED to load — FALLING BACK TO PLAINTEXT: %v", err)
			} else {
				c.Log.Log(CatMeshCell, "tls: configuration error (serving plaintext): %v", err)
			}
		}
		c.tlsConfig = cfg
		c.tlsSelfSigned = selfSigned
	})
	return c.tlsConfig, c.tlsSelfSigned
}
