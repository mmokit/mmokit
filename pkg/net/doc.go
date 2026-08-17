// Package net is the game-agnostic client transport layer. It manages byte
// frames over WebSocket and over the repository's custom UDP protocol; typed
// messages and replication live in higher-level packages, which treat this one
// as a pipe.
//
// Two frame channels are carried: 0x00 for typed events and input, 0x01 for
// typed request/response operations. This package neither encodes nor
// interprets their bodies.
//
// The UDP transport is off by default and opt-in via --udp-listen. Since
// CE-005b Tier 2 it is authenticated and encrypted: every packet is sealed with
// ChaCha20-Poly1305 under per-direction keys derived from a session key the
// client draws over HTTPS from POST /auth/udp-key, and the session is bound to
// that key's user before it carries a byte. See README.md for the packet
// layout and SECURITY.md for the residual limitations.
package net
