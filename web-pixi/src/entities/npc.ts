import { Container, Graphics, Text } from "pixi.js";
import type { NPCEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { getCombat } from "../entity-accessors";
import { px } from "../view";

// NPCAI.Archetype values — must match internal/game ArchetypeXxx consts.
// PVE v2: Sniper/Swarmer were replaced by Artillery/Kamikaze at the same
// wire values (1, 2). Kamikaze was then replaced by Lancer (telegraphed
// line-charge) at the same value (2).
const ARCH_BRAWLER = 0;
const ARCH_ARTILLERY = 1;
const ARCH_LANCER = 2;
// PVE v3 dungeon terminal-chamber main boss. Stationary, large HP pool,
// periodic add-spawn mechanic — visually distinguished as a 2× hull and
// a distinct deep-red tint so pilots immediately recognize "this is THE
// boss" vs the regular Brawler/Artillery/Lancer mob mix. Elite ("side
// boss") champions aren't yet on the wire (Elite is an NPCSpawnModifier
// only), so v1 distinguishes BossGuardian only.
const ARCH_BOSS_GUARDIAN = 3;

// NPCAI.State values — must match internal/game AIState* consts. We
// only care about Leashed (returning to leash anchor) for visual fade.
const AI_STATE_LEASH = 3;

interface ArchetypeStyle {
  color: number;
  scale: number;
  label: string;
}

const ARCHETYPES: Record<number, ArchetypeStyle> = {
  [ARCH_BRAWLER]:        { color: 0xff4444, scale: 1.2, label: "BRAWLER" },
  [ARCH_ARTILLERY]:      { color: 0xcc66ff, scale: 1.3, label: "ARTILLERY" },
  [ARCH_LANCER]:         { color: 0xffaa22, scale: 1.0, label: "LANCER" },
  [ARCH_BOSS_GUARDIAN]:  { color: 0xff1818, scale: 2.0, label: "GUARDIAN" },
};

const DEFAULT_ARCH: ArchetypeStyle = { color: 0xff4444, scale: 1.0, label: "NPC" };

export function createNpcDisplay(): EntityDisplayObject {
  const container = new Container();

  // Archetype scale lives on a child so the bars/labels (which are anchored
  // to the parent's UI container) don't get scaled with the hull. Only the
  // hull graphic should grow/shrink per archetype.
  const hullContainer = new Container();
  container.addChild(hullContainer);

  const hull = new Graphics();
  hullContainer.addChild(hull);

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

  let lastW = 0;
  let lastH = 0;
  let lastArchetype = -1;
  let style: ArchetypeStyle = DEFAULT_ARCH;

  function drawHull(hw: number, hh: number) {
    hull.clear();
    const color = style.color;

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
      const e = ent.current as NPCEntity;
      const w = e.width || 1.7;
      const h = e.height || 0.83;
      const hw = w / 2;
      const hh = h / 2;

      // Resolve archetype-specific style on first observation. Archetype is
      // tagged `net:"initial,u8"` on the server so it arrives in the entered
      // entity payload and never changes after spawn. Skip the update if the
      // value isn't decoded yet — avoids a brief "NPC" placeholder flash on
      // the first frame when the SDK delta hasn't fully populated the field.
      const arch = e.archetype;
      if (arch !== undefined && arch !== null && ARCHETYPES[arch] !== undefined && arch !== lastArchetype) {
        lastArchetype = arch;
        style = ARCHETYPES[arch];
        hullContainer.scale.set(style.scale);
        nameTag.text = style.label;
        nameTag.style.fill = style.color;
        // Force a hull redraw so the new color sticks.
        lastW = -1;
        lastH = -1;
      }
      // Hide the label until we've resolved a valid archetype — prevents the
      // DEFAULT_ARCH "NPC" placeholder from rendering on entities that just
      // entered AoI but haven't decoded their initial payload yet.
      nameTag.visible = lastArchetype !== -1;

      if (w !== lastW || h !== lastH) {
        lastW = w;
        lastH = h;
        drawHull(hw, hh);
      }

      // Leashed NPCs (returning to anchor) fade to ~0.5 alpha and become
      // visually un-targetable. State is replicated `net:"u8"` so it
      // updates on every AI transition.
      const leashed = e.state === AI_STATE_LEASH;
      container.alpha = leashed ? 0.5 : 1.0;
      uiContainer.alpha = leashed ? 0.5 : 1.0;

      // UI bars
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
