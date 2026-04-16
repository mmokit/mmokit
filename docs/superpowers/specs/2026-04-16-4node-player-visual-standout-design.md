---
title: 4node-basic — player visual standout
date: 2026-04-16
scope: examples/4node-basic/web/src/renderer.ts
---

# Goal

Make the local player visually unmistakable and always on top of other entities (notably bot-command entities from `bot spawn`), while preserving the existing cell-based fill color that ties entities to their owning cell.

# Changes

All changes confined to `examples/4node-basic/web/src/renderer.ts`, section 4 ("Entities").

## 1. Two-pass render order

Split the single entity loop into two passes over `state.entities`:

1. **Pass A** — draw every entity where `netID !== state.playerNetID`.
2. **Pass B** — draw the player entity (if present).

Guarantees the player is painted last and therefore always on top, without needing a Z-sort. No behavior change for non-player entities.

## 2. Player size bump

When `isPlayer` is true, multiply the rendered radius by **1.4×** before drawing the circle, velocity arrow anchor, net-ID label offset, name offset, and debug badge offset. Only the *render* radius changes; `ent.radius` (AoI / logic) is untouched.

## 3. Player glow halo

Before drawing the player's filled circle, draw a soft outer glow:

- A radial gradient from `rgba(255,255,255,0.35)` at inner edge (player radius) to `rgba(255,255,255,0)` at `radius * 2.0`.
- Painted as a filled circle of radius `radius * 2.0` centered on the player.
- Uses `ctx.save()` / `ctx.restore()` to avoid leaking state.

The glow sits *under* the filled circle so the existing cell-based fill + white stroke render unchanged on top of it.

# Non-goals

- No change to bot rendering.
- No change to debug overlays, legend, HUD, or cell rendering.
- No change to server-side logic or protocol.

# Verification

Manual: run `cd examples/4node-basic && just dev`, log in, `bot spawn 40`, confirm:
1. Player is visibly larger than bots.
2. Player has a subtle glow halo.
3. Player fill color still matches its current cell (debug-mode tint).
4. When a bot overlaps the player, the player renders on top.
