// Package chat is mmokit's engine-tier chat service.
//
// Single-instance v1: RAM-authoritative for transient state (subscriptions,
// rate buckets, online presence, message-id TTL index) and Postgres-backed
// for durable state (channel definitions, memberships, mutes). Messages and
// DMs are pure pass-through — no ring buffer, no recent-history-on-join.
//
// Wired into a game with one line:
//
//	mmokit.RegisterChatService(coord, mmokit.ChatOpts{
//	    DefaultChannels: []mmokit.DefaultChannelDef{
//	        {Slug: "world", Kind: mmokit.ChannelKindSystemAll, Topic: "World chat"},
//	    },
//	})
//
// See docs/superpowers/specs/2026-05-07-chat-service-design.md.
package chat
