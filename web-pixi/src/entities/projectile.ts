import { Container, Graphics } from "pixi.js";
import type { ProjectileEntity } from "../../sdk/index.js";
import { EntityType } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

// Server projectile type discriminator (must match
// internal/component/projectile.go ProjectileType enum).
const PROJECTILE_PLASMA = 0;
const PROJECTILE_MISSILE = 1;
const PROJECTILE_MORTAR = 2;

interface VariantStyle {
  color: number;
  trailColor: number;
  radius: number; // in screen px
}

const VARIANT_STYLES: Record<number, VariantStyle> = {
  [PROJECTILE_PLASMA]: { color: 0x44e0ff, trailColor: 0x1488aa, radius: 3 },
  [PROJECTILE_MISSILE]: { color: 0xff9933, trailColor: 0xaa5511, radius: 4 },
  [PROJECTILE_MORTAR]: { color: 0xffdd33, trailColor: 0xaa8811, radius: 5 },
};

// createProjectileDisplay renders a server-spawned Projectile entity.
// Visual variant (color + size) is driven by ProjectileEntity.type so
// the same renderer covers Plasma / Missile / Mortar without per-kind
// dispatch. Position/rotation are set by EntityManager from the
// interpolated entity state — the renderer only draws shape + trail.
export function createProjectileDisplay(): EntityDisplayObject {
  const container = new Container();
  const trail = new Graphics();
  const body = new Graphics();
  container.addChild(trail);
  container.addChild(body);

  let lastType = -1;

  function redraw(style: VariantStyle) {
    body.clear();
    body.circle(0, 0, px(style.radius)).fill({ color: style.color });
    // Bright core highlight to make the projectile pop against dark
    // space — picks up the same hue at higher alpha.
    body.circle(0, 0, px(style.radius * 0.5)).fill({ color: 0xffffff, alpha: 0.7 });

    trail.clear();
    // Stubby tail behind the projectile. Rotation is owned by
    // EntityManager (sets container.rotation from renderRot), so the
    // tail just points -X in local space.
    const tailLen = px(style.radius * 4);
    const tailWidth = px(style.radius * 0.9);
    trail.poly([
      -px(style.radius * 0.5), -tailWidth,
      -tailLen, 0,
      -px(style.radius * 0.5), tailWidth,
    ]).fill({ color: style.trailColor, alpha: 0.55 });
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, _now: number) {
      if (ent.current.entityType !== EntityType.Projectile) return;
      const e = ent.current as ProjectileEntity;
      if (e.type !== lastType) {
        lastType = e.type;
        const style = VARIANT_STYLES[e.type] ?? VARIANT_STYLES[PROJECTILE_PLASMA];
        redraw(style);
      }
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}

// createUnknownEntityDisplay renders a fallback dot for entity kinds the
// client doesn't recognize (forward-compat with newer server entity
// kinds). Kept colocated with the projectile renderer since both are
// minimal Graphics-based markers.
export function createUnknownEntityDisplay(color: number): EntityDisplayObject {
  const container = new Container();
  const gfx = new Graphics();
  container.addChild(gfx);
  gfx.poly([px(6), 0, -px(3), -px(2), -px(3), px(2)]).fill({ color });
  return {
    container,
    update(_ent: ClientEntity, _isMe: boolean, _now: number) {},
    destroy() {
      container.destroy({ children: true });
    },
  };
}
