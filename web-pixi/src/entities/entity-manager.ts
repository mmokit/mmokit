import { Container } from "pixi.js";
import { EntityType } from "@gen/game_pb.js";
import { ENTITY_COLORS, RESOURCE_COLORS_HEX } from "../constants";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { getAsteroid } from "../entity-accessors";
import { createShipDisplay } from "./ship";
import { createAsteroidDisplay } from "./asteroid";
import { createProjectileDisplay } from "./projectile";
import { createStationDisplay } from "./station";
import { createLootCrateDisplay } from "./loot-crate";
import { createNpcDisplay } from "./npc";

export class EntityManager {
  private displayObjects = new Map<number, EntityDisplayObject>();
  private entityContainer: Container;
  private uiContainer: Container; // Non-rotating layer for ship bars/names

  constructor(entityContainer: Container, uiContainer: Container) {
    this.entityContainer = entityContainer;
    this.uiContainer = uiContainer;
  }

  sync(entities: Map<number, ClientEntity>, myEntityId: number, now: number): void {
    // Remove display objects for entities no longer present
    for (const [id, obj] of this.displayObjects) {
      if (!entities.has(id)) {
        this.entityContainer.removeChild(obj.container);
        // Remove UI container if ship
        if ("uiContainer" in obj) {
          this.uiContainer.removeChild((obj as any).uiContainer);
        }
        obj.destroy();
        this.displayObjects.delete(id);
      }
    }

    // Add/update display objects
    for (const [id, ent] of entities) {
      let obj = this.displayObjects.get(id);

      if (!obj) {
        obj = this.createDisplayObject(ent);
        this.displayObjects.set(id, obj);
        this.entityContainer.addChild(obj.container);
        // Add ship UI containers to non-rotating layer
        if ("uiContainer" in obj) {
          this.uiContainer.addChild((obj as any).uiContainer);
        }
      }

      const isMe = id === myEntityId;

      // Update position/rotation
      obj.container.position.set(ent.renderX, ent.renderY);
      // Loot crates handle their own rotation in update()
      if (ent.curr.entityType !== EntityType.LOOT_CRATE) {
        obj.container.rotation = ent.renderRot;
      }

      // Entity-specific updates
      obj.update(ent, isMe, now);
    }
  }

  private createDisplayObject(ent: ClientEntity): EntityDisplayObject {
    const e = ent.curr;
    switch (e.entityType) {
      case EntityType.SHIP:
        return createShipDisplay();
      case EntityType.ASTEROID:
        return createAsteroidDisplay(getAsteroid(e)?.resourceType ?? 0, e.radius || 0.7);
      case EntityType.PROJECTILE: {
        const color = ENTITY_COLORS[EntityType.PROJECTILE] || 0xffff44;
        return createProjectileDisplay(color);
      }
      case EntityType.STATION:
        return createStationDisplay(e.radius || 5);
      case EntityType.LOOT_CRATE:
        return createLootCrateDisplay(e.radius || 0.4);
      case EntityType.NPC:
        return createNpcDisplay();
      default: {
        const color = ENTITY_COLORS[e.entityType] || 0xffffff;
        return createProjectileDisplay(color);
      }
    }
  }

  clear(): void {
    for (const [, obj] of this.displayObjects) {
      this.entityContainer.removeChild(obj.container);
      if ("uiContainer" in obj) {
        this.uiContainer.removeChild((obj as any).uiContainer);
      }
      obj.destroy();
    }
    this.displayObjects.clear();
  }
}
