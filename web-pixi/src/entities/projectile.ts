import { Container, Graphics } from "pixi.js";
import type { ClientEntity, EntityDisplayObject } from "../types";

export function createProjectileDisplay(color: number): EntityDisplayObject {
  const container = new Container();
  const gfx = new Graphics();
  container.addChild(gfx);

  gfx.poly([6, 0, -3, -2, -3, 2]).fill({ color });

  return {
    container,
    update(_ent: ClientEntity, _isMe: boolean, _now: number) {
      // Static shape, nothing to update
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
