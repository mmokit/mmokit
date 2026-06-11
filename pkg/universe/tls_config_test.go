package universe

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
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
