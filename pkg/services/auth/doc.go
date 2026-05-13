// Package auth provides mmokit's engine-tier identity service.
//
// Auth is registered as a pluggable service kind via mmokit.RegisterAuthService.
// Every game built on mmokit gets production-grade auth:
//   - argon2id password storage
//   - opaque 256-bit session tokens with sliding TTL
//   - per-IP token-bucket rate limiting
//   - per-account lockout (DB-persistent)
//   - audit log
//   - schema seam for OIDC v2 (auth.identities table present from day one)
//
// Auth is wire-protocol-agnostic. Per-connection / per-message authentication
// (UDP AEAD, DTLS, sequence + replay window) is the gateway's concern; the
// auth service knows nothing about it.
//
// See docs/superpowers/specs/2026-05-01-auth-service-design.md for the
// full design.
package auth
