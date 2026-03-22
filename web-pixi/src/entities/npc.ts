import { Container, Graphics, Text } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { getCombat } from "../entity-accessors";
import { px } from "../view";

export function createNpcDisplay(): EntityDisplayObject {
  const container = new Container();

  const hull = new Graphics();
  container.addChild(hull);

  // Screen-space container (not rotated) for bars and label
  const uiContainer = new Container();

  // NPC label
  const nameTag = new Text({ text: "NPC", style: { fontFamily: "monospace", fontSize: 10, fill: 0xff6666 } });
  nameTag.anchor.set(0.5, 1);
  nameTag.scale.set(px(1), px(1));
  uiContainer.addChild(nameTag);

  // Shield bar
  const shieldBarBg = new Graphics();
  const shieldBarFill = new Graphics();
  uiContainer.addChild(shieldBarBg);
  uiContainer.addChild(shieldBarFill);

  // HP bar
  const hpBarBg = new Graphics();
  const hpBarFill = new Graphics();
  uiContainer.addChild(hpBarBg);
  uiContainer.addChild(hpBarFill);

  const color = 0xff4444;
  let lastW = 0;
  let lastH = 0;

  function drawHull(hw: number, hh: number) {
    hull.clear();

    // Angular hostile hull shape — diamond/chevron
    hull.poly([
      hw, 0,
      hw * 0.3, -hh * 0.8,
      -hw * 0.2, -hh * 1.1,
      -hw * 0.6, -hh * 0.7,
      -hw * 0.7, 0,
      -hw * 0.6, hh * 0.7,
      -hw * 0.2, hh * 1.1,
      hw * 0.3, hh * 0.8,
    ]);
    hull.fill({ color, alpha: 0.15 });

    hull.poly([
      hw, 0,
      hw * 0.3, -hh * 0.8,
      -hw * 0.2, -hh * 1.1,
      -hw * 0.6, -hh * 0.7,
      -hw * 0.7, 0,
      -hw * 0.6, hh * 0.7,
      -hw * 0.2, hh * 1.1,
      hw * 0.3, hh * 0.8,
    ]);
    hull.stroke({ color, width: px(2), alpha: 1 });

    // Center line
    hull.moveTo(hw * 0.8, 0).lineTo(-hw * 0.6, 0).stroke({ color, width: px(1), alpha: 0.25 });

    // Wing spars
    hull.moveTo(0, -hh * 0.6).lineTo(-hw * 0.5, -hh * 0.9).stroke({ color, width: px(1), alpha: 0.3 });
    hull.moveTo(0, hh * 0.6).lineTo(-hw * 0.5, hh * 0.9).stroke({ color, width: px(1), alpha: 0.3 });
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, _now: number) {
      const e = ent.curr;
      const w = e.width || 1.7;
      const h = e.height || 0.83;
      const hw = w / 2;
      const hh = h / 2;

      if (w !== lastW || h !== lastH) {
        lastW = w;
        lastH = h;
        drawHull(hw, hh);
      }

      // UI bars
      const barW = Math.max(w, px(40));
      const barH = px(3);
      const barGap = px(2);
      const shipTopOffset = -Math.sqrt(hw * hw + hh * hh) - px(24);

      // Shield bar
      const combat = getCombat(e);
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

      // Label
      nameTag.position.set(0, shY - px(4));

      // Position UI container at entity position
      uiContainer.position.set(ent.renderX, ent.renderY);
    },
    destroy() {
      container.destroy({ children: true });
      uiContainer.destroy({ children: true });
    },
    get uiContainer() { return uiContainer; },
  } as EntityDisplayObject & { uiContainer: Container };
}
