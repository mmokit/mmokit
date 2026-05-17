import { Container, Graphics, Text } from "pixi.js";
import type { DungeonEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// Hardcoded entrance pattern — mirrors the server-side layout in
// internal/game/entity_dungeon.go::SpawnDungeonFromGraph. The
// entrance positions themselves aren't on the wire (the Dungeon
// component's EntranceX/Y fields are `mmokit:"local"`), so the
// client reproduces the same south-biased trio (south, NE, NW)
// from the radius. EntranceCount is also local-only, so we just
// render all three for the v1 dungeon layout.
//
// If/when the codec gains f32 initial-field support, the entrance
// list should come from the wire so the client follows the server's
// pickEntranceAngles outputs exactly (random-rotated variants etc.).
function entrancePositions(radius: number): Array<{ x: number; y: number }> {
  const cos30 = Math.cos(Math.PI / 6);
  const sin30 = Math.sin(Math.PI / 6);
  return [
    { x: 0, y: -radius },                 // south (server uses Y-down for these constants)
    { x: radius * cos30, y: -radius * sin30 },  // NE
    { x: -radius * cos30, y: -radius * sin30 }, // NW
  ];
}

export function createDungeonDisplay(): EntityDisplayObject {
  const container = new Container();

  // The asteroid silhouette draws once at first valid radius and never
  // redraws (geometry is static — radius is set at spawn time and the
  // jitter pattern is deterministic from a fixed seed below).
  const rock = new Graphics();
  container.addChild(rock);

  // Entrance markers — small colored notches at each cave-mouth position.
  // These don't truly "punch out" the rock silhouette (no even-odd fill
  // in v1), they're an unmistakable colored dot + ring so pilots can spot
  // entry points without studying the wall layout.
  const entrances = new Graphics();
  container.addChild(entrances);

  // Name label hovers above the asteroid.
  const label = new Text({
    text: "DUNGEON",
    style: {
      fontFamily: "monospace",
      fontSize: 18,
      fontWeight: "bold",
      fill: 0xffaa55,
    },
  });
  label.anchor.set(0.5, 1);
  label.scale.set(px(1), px(1));
  container.addChild(label);

  const sublabel = new Text({
    text: "[ ENTER VIA CAVE MOUTHS ]",
    style: { fontFamily: "monospace", fontSize: 11, fill: 0xffaa55 },
  });
  sublabel.anchor.set(0.5, 1);
  sublabel.scale.set(px(1), px(1));
  sublabel.alpha = 0.4;
  container.addChild(sublabel);

  let drawnRadius = 0;
  let drawnName = "";

  function drawRock(radius: number) {
    rock.clear();
    entrances.clear();

    // Irregular silhouette — 32-sided polygon with deterministic per-vertex
    // jitter so the outline looks rocky but is identical across clients.
    const sides = 32;
    const points: number[] = [];
    for (let i = 0; i < sides; i++) {
      const angle = (i / sides) * Math.PI * 2;
      // Deterministic jitter — uses i directly so every client sees the
      // same outline. Range ~ [0.88, 1.0] of the outer radius.
      const jitter =
        0.94 + Math.sin(i * 1.37) * 0.04 + Math.cos(i * 2.71 + 1.3) * 0.025;
      const r = radius * jitter;
      points.push(Math.cos(angle) * r, Math.sin(angle) * r);
    }
    rock
      .poly(points)
      .fill({ color: 0x1a1612, alpha: 1.0 })
      .stroke({ color: 0x4a3a2a, width: px(2), alpha: 0.8 });

    // Inner detail rings — pock-mark hint of crater geometry.
    rock
      .circle(0, 0, radius * 0.78)
      .stroke({ color: 0x3a2e22, width: px(1), alpha: 0.4 });
    rock
      .circle(0, 0, radius * 0.55)
      .stroke({ color: 0x3a2e22, width: px(1), alpha: 0.25 });

    // Scattered craters — fixed seed via deterministic angles.
    for (let i = 0; i < 12; i++) {
      const angle = i * 0.523 + Math.sin(i * 3.1) * 0.4;
      const distR = radius * (0.3 + ((i * 0.137) % 0.5));
      const cR = radius * (0.04 + ((i * 0.07) % 0.06));
      rock
        .circle(Math.cos(angle) * distR, Math.sin(angle) * distR, cR)
        .fill({ color: 0x0e0a08, alpha: 0.6 });
    }

    // Entrance markers — orange glowing notch + bright dot.
    const ents = entrancePositions(radius);
    for (const e of ents) {
      // Outer glow halo
      entrances
        .circle(e.x, e.y, radius * 0.06)
        .fill({ color: 0xff8833, alpha: 0.2 });
      // Mid ring
      entrances
        .circle(e.x, e.y, radius * 0.035)
        .fill({ color: 0xffaa55, alpha: 0.45 })
        .stroke({ color: 0xffcc88, width: px(1.5), alpha: 0.9 });
      // Bright core dot
      entrances
        .circle(e.x, e.y, radius * 0.012)
        .fill({ color: 0xffeeaa, alpha: 1.0 });
    }
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      const e = ent.current as DungeonEntity;
      const radius = e.radius || 1800;

      if (radius !== drawnRadius) {
        drawnRadius = radius;
        drawRock(radius);
        // Position labels relative to the asteroid's outer radius.
        label.position.set(0, -radius - px(18));
        sublabel.position.set(0, -radius - px(2));
      }
      if (e.name && e.name !== drawnName) {
        drawnName = e.name;
        label.text = e.name.toUpperCase();
      }

      // Pulse the entrance markers so they read as "active" / "inviting".
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.0025);
      entrances.alpha = pulse;
      label.alpha = 0.5 + pulse * 0.3;
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
