// Package net is the game-agnostic client transport layer. It manages byte
// frames over WebSocket and over the repository's custom UDP protocol; typed
// messages and replication live in higher-level packages, which treat this one
// as a pipe.
//
// Two frame channels are carried: 0x00 for typed events and input, 0x01 for
// typed request/response operations. This package neither encodes nor
// interprets their bodies.
//
// The UDP transport is experimental and is off by default. It is neither
// authenticated nor encrypted; see SECURITY.md before enabling it.
package net
