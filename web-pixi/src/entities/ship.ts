import { Container, Graphics, Text } from "pixi.js";
import type { ShipEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { getCombat, getShip } from "../entity-accessors";
import { px } from "../view";

export function createShipDisplay(): EntityDisplayObject {
  const container = new Container();

  // Exhaust layer (behind hull)
  const exhaust = new Graphics();
  container.addChild(exhaust);

  // Hull layer
  const hull = new Graphics();
  container.addChild(hull);

  // Nav lights
  const navLights = new Graphics();
  container.addChild(navLights);

  // Screen-space container (not rotated) for bars and name
  const uiContainer = new Container();
  // We'll position this separately each frame

  // Name tag
  const nameTag = new Text({ text: "", style: { fontFamily: "monospace", fontSize: 10, fill: 0xccddff } });
  nameTag.anchor.set(0.5, 1);
  nameTag.scale.set(px(1), px(1));
  uiContainer.addChild(nameTag);

  // Shield bar background + fill
  const shieldBarBg = new Graphics();
  const shieldBarFill = new Graphics();
  uiContainer.addChild(shieldBarBg);
  uiContainer.addChild(shieldBarFill);

  // HP bar background + fill
  const hpBarBg = new Graphics();
  const hpBarFill = new Graphics();
  uiContainer.addChild(hpBarBg);
  uiContainer.addChild(hpBarFill);

  // Supercruise channel radial (local player only, drawn while phase===1)
  const channelRadial = new Graphics();
  uiContainer.addChild(channelRadial);

  let currentColor = 0x44aaff;
  let lastW = 0;
  let lastH = 0;

  function drawHull(hw: number, hh: number, color: number) {
    hull.clear();

    // Hull fill
    hull.poly([
      hw, 0,
      hw * 0.55, -hh * 0.42,
      hw * 0.1, -hh * 0.52,
      -hw * 0.1, -hh * 0.6,
      -hw * 0.25, -hh * 1.25,
      -hw * 0.55, -hh * 1.1,
      -hw * 0.7, -hh * 0.5,
      -hw * 0.7, hh * 0.5,
      -hw * 0.55, hh * 1.1,
      -hw * 0.25, hh * 1.25,
      -hw * 0.1, hh * 0.6,
      hw * 0.1, hh * 0.52,
      hw * 0.55, hh * 0.42,
    ]);
    hull.fill({ color, alpha: 0.1 });

    // Hull stroke
    hull.poly([
      hw, 0,
      hw * 0.55, -hh * 0.42,
      hw * 0.1, -hh * 0.52,
      -hw * 0.1, -hh * 0.6,
      -hw * 0.25, -hh * 1.25,
      -hw * 0.55, -hh * 1.1,
      -hw * 0.7, -hh * 0.5,
      -hw * 0.7, hh * 0.5,
      -hw * 0.55, hh * 1.1,
      -hw * 0.25, hh * 1.25,
      -hw * 0.1, hh * 0.6,
      hw * 0.1, hh * 0.52,
      hw * 0.55, hh * 0.42,
    ]);
    hull.stroke({ color, width: px(2), alpha: 1 });

    // Panel lines
    hull.moveTo(hw * 0.6, 0).lineTo(-hw * 0.65, 0).stroke({ color, width: px(1), alpha: 0.2 });
    hull.moveTo(hw * 0.3, -hh * 0.35).lineTo(hw * 0.3, hh * 0.35).stroke({ color, width: px(1), alpha: 0.2 });
    hull.moveTo(-hw * 0.1, -hh * 0.55).lineTo(-hw * 0.1, hh * 0.55).stroke({ color, width: px(1), alpha: 0.2 });
    hull.moveTo(-hw * 0.5, -hh * 0.7).lineTo(-hw * 0.5, hh * 0.7).stroke({ color, width: px(1), alpha: 0.2 });

    // Wing spars
    hull.moveTo(0, -hh * 0.55).lineTo(-hw * 0.45, -hh * 1.05).stroke({ color, width: px(1), alpha: 0.3 });
    hull.moveTo(0, hh * 0.55).lineTo(-hw * 0.45, hh * 1.05).stroke({ color, width: px(1), alpha: 0.3 });

    // Wing panel fills
    hull.poly([
      -hw * 0.1, -hh * 0.6,
      -hw * 0.25, -hh * 1.25,
      -hw * 0.55, -hh * 1.1,
      -hw * 0.35, -hh * 0.6,
    ]).fill({ color, alpha: 0.15 });
    hull.poly([
      -hw * 0.1, hh * 0.6,
      -hw * 0.25, hh * 1.25,
      -hw * 0.55, hh * 1.1,
      -hw * 0.35, hh * 0.6,
    ]).fill({ color, alpha: 0.15 });

    // Cockpit canopy
    hull.poly([
      hw * 0.8, 0,
      hw * 0.35, -hh * 0.22,
      hw * 0.15, -hh * 0.18,
      hw * 0.15, hh * 0.18,
      hw * 0.35, hh * 0.22,
    ]).fill({ color, alpha: 0.35 });
    hull.poly([
      hw * 0.8, 0,
      hw * 0.35, -hh * 0.22,
      hw * 0.15, -hh * 0.18,
      hw * 0.15, hh * 0.18,
      hw * 0.35, hh * 0.22,
    ]).stroke({ color, width: px(1), alpha: 0.5 });

    // Engine housings
    hull.rect(-hw * 0.72, -hh * 0.48, hw * 0.12, hh * 0.35)
      .fill({ color, alpha: 0.15 })
      .stroke({ color, width: px(1), alpha: 0.4 });
    hull.rect(-hw * 0.72, hh * 0.13, hw * 0.12, hh * 0.35)
      .fill({ color, alpha: 0.15 })
      .stroke({ color, width: px(1), alpha: 0.4 });

    // Nose accent
    hull.moveTo(hw * 0.85, 0).lineTo(hw * 0.55, -hh * 0.4).stroke({ color, width: px(2), alpha: 0.15 });
    hull.moveTo(hw * 0.85, 0).lineTo(hw * 0.55, hh * 0.4).stroke({ color, width: px(2), alpha: 0.15 });
  }

  return {
    container,
    update(ent: ClientEntity, isMe: boolean, now: number) {
      const e = ent.current as ShipEntity;
      const w = e.width || 2;
      const h = e.height || 1;
      const hw = w / 2;
      const hh = h / 2;
      const color = isMe ? 0x00ff00 : 0x44aaff;
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);

      // Rebuild hull if color or size changed
      if (color !== currentColor || w !== lastW || h !== lastH) {
        currentColor = color;
        lastW = w;
        lastH = h;
        drawHull(hw, hh, color);
      }

      // Thrusting detection
      let thrusting = false;
      if (isMe) {
        // Will be set from outside via thruster particle system
        const spd = Math.sqrt(e.velX * e.velX + e.velY * e.velY);
        thrusting = spd > 1;
      } else {
        const spd = Math.sqrt(e.velX * e.velX + e.velY * e.velY);
        thrusting = spd > 1;
      }

      // Exhaust
      exhaust.clear();
      if (thrusting) {
        const flicker = 0.7 + 0.3 * Math.sin(now * 0.015);

        // Outer plume
        exhaust.poly([
          -hw * 0.7, -hh * 0.55,
          -hw * (1.3 + flicker * 0.3), 0,
          -hw * 0.7, hh * 0.55,
        ]).fill({ color: 0x3c78ff, alpha: 0.1 });

        // Main glow
        exhaust.poly([
          -hw * 0.7, -hh * 0.4,
          -hw * (1.1 + flicker * 0.25), 0,
          -hw * 0.7, hh * 0.4,
        ]).fill({ color: 0x5096ff, alpha: 0.2 + flicker * 0.25 });

        // Hot core
        exhaust.poly([
          -hw * 0.72, -hh * 0.12,
          -hw * (0.9 + flicker * 0.15), 0,
          -hw * 0.72, hh * 0.12,
        ]).fill({ color: 0xc8e6ff, alpha: 0.3 + flicker * 0.4 });
      }

      // Engine nozzle glow
      const nozzleAlpha = thrusting ? 0.6 + pulse * 0.4 : 0.15;
      exhaust.rect(-hw * 0.73, -hh * 0.38, hw * 0.04, hh * 0.22).fill({ color: 0x5096ff, alpha: nozzleAlpha });
      exhaust.rect(-hw * 0.73, hh * 0.16, hw * 0.04, hh * 0.22).fill({ color: 0x5096ff, alpha: nozzleAlpha });

      // Nav lights
      navLights.clear();
      const navAlpha = 0.5 + pulse * 0.3;

      // Wing tips
      for (const wy of [-hh * 1.23, hh * 1.23]) {
        navLights.circle(-hw * 0.25, wy, px(1.5)).fill({ color: 0xffffff, alpha: navAlpha });
      }
      // Nose
      navLights.circle(hw * 0.95, 0, px(1)).fill({ color: 0xffffff, alpha: navAlpha });
      // Tail
      navLights.circle(-hw * 0.68, 0, px(1.5)).fill({ color: 0xffffff, alpha: navAlpha * 0.7 });

      // Update UI elements (screen-space — we position uiContainer relative to entity)
      const barW = Math.max(w, px(40));
      const barH = px(3);
      const barGap = px(2);
      const shipTopOffset = -Math.sqrt(hw * hw + hh * hh) - px(24);

      // Shield bar
      const combat = getCombat(ent);
      const shY = shipTopOffset - barH * 2 - barGap;
      shieldBarBg.clear().rect(-barW / 2, shY, barW, barH).fill({ color: 0x5082ff, alpha: 0.15 });
      const shFrac = combat && combat.maxShield > 0 ? combat.shield / combat.maxShield : 0;
      shieldBarFill.clear();
      if (shFrac > 0) {
        shieldBarFill.rect(-barW / 2, shY, barW * shFrac, barH).fill({ color: 0x5082ff, alpha: 0.9 });
      }

      // HP bar
      const hpY = shipTopOffset - barH;
      hpBarBg.clear().rect(-barW / 2, hpY, barW, barH).fill({ color: 0xff3c3c, alpha: 0.15 });
      const hpFrac = combat && combat.maxHealth > 0 ? combat.health / combat.maxHealth : 0;
      hpBarFill.clear().rect(-barW / 2, hpY, barW * hpFrac, barH).fill({ color: hpFrac > 0.3 ? 0xff3c3c : 0xff1e1e, alpha: 0.9 });

      // Name
      const shipData = getShip(ent);
      if (shipData?.name) {
        nameTag.text = shipData.name;
        nameTag.style.fill = isMe ? 0x00ff00 : 0xccddff;
        nameTag.position.set(0, shY - px(4));
        nameTag.visible = true;
      } else {
        nameTag.visible = false;
      }

      // Supercruise channel radial — local player only, while phase===1.
      // Fills clockwise from 0 to 1 over (1 - channelRemaining/CHANNEL_TIME).
      // SUPERCRUISE_CHANNEL_TIME default = 3.0s; hard-coded here per spec.
      channelRadial.clear();
      if (isMe && e.phase === 1) {
        const CHANNEL_TIME = 3.0;
        const remaining = e.channelRemaining;
        const progress = Math.max(0, Math.min(1, 1 - remaining / CHANNEL_TIME));
        const radius = Math.sqrt(hw * hw + hh * hh) + px(8);
        // Background ring (faint)
        channelRadial.circle(0, 0, radius).stroke({ color: 0x66aaff, width: px(2), alpha: 0.25 });
        // Progress arc, starting at top (-PI/2), sweeping clockwise.
        if (progress > 0) {
          const startAngle = -Math.PI / 2;
          const endAngle = startAngle + progress * Math.PI * 2;
          channelRadial.arc(0, 0, radius, startAngle, endAngle, false)
            .stroke({ color: 0x88ccff, width: px(3), alpha: 0.9 });
        }
      }

      // Position UI container at entity position but without rotation
      uiContainer.position.set(ent.renderX, ent.renderY);
    },
    destroy() {
      container.destroy({ children: true });
      uiContainer.destroy({ children: true });
    },
    // Expose uiContainer for adding to a non-rotating layer
    get uiContainer() { return uiContainer; },
  } as EntityDisplayObject & { uiContainer: Container };
}
