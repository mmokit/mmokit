package universe

import (
	"crypto/tls"
	"errors"
)

// errMeshCertUnavailable guards the sync.Once race where a first caller
// errored: the Do body will not run again, so a later caller sees a nil error
// and an empty certificate. Failing loudly beats serving a mesh listener with
// no certificate at all.
var errMeshCertUnavailable = errors.New("mesh: TLS certificate generation failed earlier in this process")

// meshTLSConfig returns the server-side TLS config for this process's mesh
// listeners, generating one in-memory self-signed certificate per process
// lifetime. The certificate is never written to disk.
//
// This is deliberately NOT httpTLSConfig. That one sync.Once-memoizes the
// client-facing posture and returns nil in the shipped default, falling back
// to plaintext on error; the mesh needs a certificate even when client TLS is
// plaintext, and has no plaintext posture to fall back to.
//
// The certificate's SANs and validity are irrelevant by design — peers dial
// with InsecureSkipVerify and authenticate with the cluster secret — so
// generateDevCert is reused unchanged. Do not "fix" its localhost-only SANs:
// that would imply a verification which does not happen.
func (c *Process) meshTLSConfig() (*tls.Config, error) {
	var err error
	c.meshTLSOnce.Do(func() {
		c.meshTLSCert, err = generateDevCert()
	})
	if err != nil {
		return nil, err
	}
	if len(c.meshTLSCert.Certificate) == 0 {
		return nil, errMeshCertUnavailable
	}
	return meshServerTLSConfigFrom(c.meshTLSCert), nil
}

// meshServerTLSConfigFrom is the server-side config shape, factored out so
// tests that build a HostNetwork without a Process produce the identical
// posture rather than a lookalike.
func meshServerTLSConfigFrom(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}

// newMeshDataListener builds this process's MeshData listener with the shared
// mesh TLS posture and cluster secret. All four production call sites pass
// ":0" — a wildcard ephemeral bind on every interface, which is exactly why
// this listener must be authenticated.
func (c *Process) newMeshDataListener(host *Host, grpcAddr string) (*HostNetwork, error) {
	tlsCfg, err := c.meshTLSConfig()
	if err != nil {
		return nil, err
	}
	return NewHostNetwork(host, grpcAddr, c.Log, c.cfg.ShutdownGracePeriod, tlsCfg, c.cfg.ClusterSecret)
}

// meshClientTLSConfig is the dial-side counterpart. InsecureSkipVerify is
// intentional and load-bearing: there is no CA and no certificate
// distribution, so verification would have nothing to verify against.
// Confidentiality against a passive eavesdropper is what this buys; peer
// identity comes from the cluster secret, not from the certificate.
func meshClientTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // no CA by design; see doc comment
		MinVersion:         tls.VersionTLS12,
	}
}
