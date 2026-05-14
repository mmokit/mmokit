import type { GameState } from "../state";
import type { AnyEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";

const SELECTION_PANEL_ID = "selection-panel";

interface PanelEls {
  root: HTMLElement;
  empty: HTMLElement;
  body: HTMLElement;
  name: HTMLElement;
  type: HTMLElement;
  hpRow: HTMLElement;
  hpBar: HTMLElement;
  hpText: HTMLElement;
  distance: HTMLElement;
}

function buildPanel(): PanelEls {
  const root = document.createElement("div");
  root.id = SELECTION_PANEL_ID;
  root.className = "selection-panel";

  const empty = document.createElement("div");
  empty.className = "selection-empty";
  empty.textContent = "Nothing selected";
  root.appendChild(empty);

  const body = document.createElement("div");
  body.className = "selection-body";
  body.style.display = "none";

  const name = document.createElement("div");
  name.className = "selection-name";
  body.appendChild(name);

  const type = document.createElement("div");
  type.className = "selection-type";
  body.appendChild(type);

  const hpRow = document.createElement("div");
  hpRow.className = "selection-hp-row";
  const hpBarOuter = document.createElement("div");
  hpBarOuter.className = "selection-hp-outer";
  const hpBar = document.createElement("div");
  hpBar.className = "selection-hp-bar";
  hpBarOuter.appendChild(hpBar);
  hpRow.appendChild(hpBarOuter);
  const hpText = document.createElement("div");
  hpText.className = "selection-hp-text";
  hpRow.appendChild(hpText);
  body.appendChild(hpRow);

  const distance = document.createElement("div");
  distance.className = "selection-distance";
  body.appendChild(distance);

  root.appendChild(body);
  return { root, empty, body, name, type, hpRow, hpBar, hpText, distance };
}

function typeLabel(ent: AnyEntity): string {
  switch (ent.entityType) {
    case EntityType.Ship:           return "Ship";
    case EntityType.Asteroid:       return "Asteroid";
    case EntityType.NPC:            return `NPC: archetype ${ent.archetype}`;
    case EntityType.Station:        return "Station";
    case EntityType.LootCrate:      return "Loot crate";
    case EntityType.POI:            return "Point of interest";
    case EntityType.AoEMarker:      return "AoE";
    case EntityType.Projectile:     return "Projectile";
    case EntityType.LineTelegraph:  return "Telegraph";
    default:                        return "Object";
  }
}

function entityDisplayName(ent: AnyEntity, netID: number): string {
  // Only Ship carries a name in the wire schema; everything else falls back
  // to the net-ID. Discriminator-narrowed so we don't reach for `(ent as any).name`.
  if (ent.entityType === EntityType.Ship) return ent.name || `#${netID}`;
  return `#${netID}`;
}

interface HpStats {
  hp: number;
  hpMax: number;
}

function entityHp(ent: AnyEntity): HpStats | null {
  // Ship and NPC are the only kinds with HP in the wire schema.
  if (ent.entityType === EntityType.Ship || ent.entityType === EntityType.NPC) {
    if (ent.healthMax > 0) return { hp: ent.healthCurrent, hpMax: ent.healthMax };
  }
  return null;
}

export class SelectionPanel {
  private els: PanelEls;

  constructor(host: HTMLElement) {
    this.els = buildPanel();
    host.appendChild(this.els.root);
  }

  update(state: GameState): void {
    if (state.selectedNetID === 0) {
      this.els.empty.style.display = "block";
      this.els.body.style.display = "none";
      return;
    }
    const ent = state.entities.get(state.selectedNetID);
    if (!ent || !ent.current) {
      this.els.empty.style.display = "block";
      this.els.body.style.display = "none";
      return;
    }
    this.els.empty.style.display = "none";
    this.els.body.style.display = "block";

    const cur = ent.current;
    this.els.name.textContent = entityDisplayName(cur, state.selectedNetID);
    this.els.type.textContent = typeLabel(cur);

    const me = state.entities.get(state.myEntityId);
    if (me) {
      const dx = ent.renderX - me.renderX;
      const dy = ent.renderY - me.renderY;
      const dist = Math.sqrt(dx * dx + dy * dy);
      this.els.distance.textContent = `${dist.toFixed(0)}u`;
    } else {
      this.els.distance.textContent = "";
    }

    const hp = entityHp(cur);
    if (hp) {
      this.els.hpRow.style.display = "flex";
      const pct = Math.max(0, Math.min(1, hp.hp / hp.hpMax));
      this.els.hpBar.style.width = `${pct * 100}%`;
      this.els.hpText.textContent = `${Math.round(hp.hp)} / ${Math.round(hp.hpMax)}`;
    } else {
      // Hide the row entirely for kinds with no HP (asteroid, station, etc.)
      this.els.hpRow.style.display = "none";
    }
  }
}
