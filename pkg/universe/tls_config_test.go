package universe

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/zenion/mmokit/pkg/logger"
)

func writeTempKeyPair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	cert, err := generateDevCert()
	if err != nil {
		t.Fatalf("generateDevCert: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}); err != nil {
		t.Fatal(err)
	}
	certOut.Close()
	keyBytes, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatal(err)
	}
	keyOut.Close()
	return certPath, keyPath
}

func TestGenerateDevCert(t *testing.T) {
	cert, err := generateDevCert()
	if err != nil {
		t.Fatalf("generateDevCert: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("no certificate bytes")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, d := range leaf.DNSNames {
		if d == "localhost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected localhost in DNSNames, got %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) == 0 {
		t.Error("expected loopback IP SANs")
	}
}

func TestResolveTLSConfig_Plaintext(t *testing.T) {
	cfg, self, err := resolveTLSConfig("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Error("expected nil tls.Config for plaintext")
	}
	if self {
		t.Error("expected selfSigned=false")
	}
}

func TestResolveTLSConfig_SelfSigned(t *testing.T) {
	cfg, self, err := resolveTLSConfig("", "", "self-signed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected a certificate")
	}
	if !self {
		t.Error("expected selfSigned=true")
	}
}

func TestResolveTLSConfig_Files(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTempKeyPair(t, dir)
	cfg, self, err := resolveTLSConfig(certPath, keyPath, "")
	if err != nil {
		t.Fatalf("load files: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected a certificate")
	}
	if self {
		t.Error("expected selfSigned=false for explicit files")
	}
}

func TestResolveTLSConfig_FilesWinOverMode(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTempKeyPair(t, dir)
	_, self, err := resolveTLSConfig(certPath, keyPath, "self-signed")
	if err != nil {
		t.Fatal(err)
	}
	if self {
		t.Error("explicit files must win over self-signed mode")
	}
}

func TestIsLoopbackBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":   true,
		"localhost:8080":   true,
		"[::1]:8080":       true,
		":8080":            false,
		"0.0.0.0:8080":     false,
		"192.168.1.5:9101": false,
	}
	for addr, want := range cases {
		if got := isLoopbackBind(addr); got != want {
			t.Errorf("isLoopbackBind(%q)=%v want %v", addr, got, want)
		}
	}
}

func TestHTTPTLSConfig_MemoizesSamePointer(t *testing.T) {
	p := &Process{cfg: Config{TLSMode: "self-signed"}, Log: logger.New()}
	cfg1, self1 := p.httpTLSConfig()
	cfg2, self2 := p.httpTLSConfig()
	if cfg1 == nil {
		t.Fatal("expected a tls.Config for self-signed mode")
	}
	if cfg1 != cfg2 {
		t.Error("httpTLSConfig must return the same memoized *tls.Config on repeated calls (both listeners share one cert)")
	}
	if !self1 || !self2 {
		t.Errorf("expected selfSigned=true on both calls, got %v / %v", self1, self2)
	}
}

func TestHTTPTLSConfig_BadFilesFallBackToPlaintext(t *testing.T) {
	p := &Process{cfg: Config{TLSCertFile: "/nonexistent/cert.pem", TLSKeyFile: "/nonexistent/key.pem"}, Log: logger.New()}
	cfg, self := p.httpTLSConfig()
	if cfg != nil {
		t.Error("expected nil tls.Config (fallback to plaintext) when the explicit keypair fails to load")
	}
	if self {
		t.Error("expected selfSigned=false on load failure")
	}
}
