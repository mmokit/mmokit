import { Container, Graphics } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";

export function createLootCrateDisplay(radius: number): EntityDisplayObject {
  const container = new Container();
  const gfx = new Graphics();
  container.addChild(gfx);

  const r = radius * 2;

  return {
    container,
    update(_ent: ClientEntity, _isMe: boolean, now: number) {
      const spin = now * 0.002;
      const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);

      container.rotation = spin;

      gfx.clear();
      gfx.poly([0, -r, r * 0.6, 0, 0, r, -r * 0.6, 0])
        .fill({ color: 0xffdd00, alpha: 0.15 * pulse })
        .stroke({ color: 0xffdd00, width: 2, alpha: 0.8 * pulse });
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
