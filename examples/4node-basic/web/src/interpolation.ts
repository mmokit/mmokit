import type { AnyEntity } from "../sdk/entities.js";
import { lerp, lerpAngle } from "../sdk/_core/interpolation-core.js";
import { InterpolationBuffer } from "../sdk/_core/interpolation-buffer.js";
import type { AdaptivePlaybackController } from "../sdk/_core/playback-controller.js";
import { MAX_EXTRAPOLATE_MS, RENDER_DELAY, RING_SIZE } from "./constants.js";
import type { ClientEntity, EntitySample } from "./state.js";
import { recordEntityCreate, type ReplicationAudit } from "./replicationAudit.js";

function entityRotation(e: AnyEntity, fallbackPrev: number): number {
  const moving = e.velX !== 0 || e.velY !== 0;
  return moving ? Math.atan2(e.velY, e.velX) : fallbackPrev;
}

function sampleFrom(e: AnyEntity, producedAtMs: number, prevRot: number): EntitySample {
  return {
    worldX: e.worldX,
    worldY: e.worldY,
    velX: e.velX,
    velY: e.velY,
    rotation: entityRotation(e, prevRot),
    producedAtMs,
    authorityEpoch: e.authorityEpoch,
  };
}

function newBuffer(): InterpolationBuffer {
  return new InterpolationBuffer({
    ringSize: RING_SIZE,
    renderDelayMs: RENDER_DELAY,
    maxExtrapolateMs: MAX_EXTRAPOLATE_MS,
  });
}

/**
 * updateEntityFromServer pushes one new authoritative snapshot into
 * the entity's buffer (creating the ClientEntity if it doesn't exist
 * yet). The per-entity producedAtMs stamp lets the render loop
 * interpolate on true ClusterClock-aligned server-time deltas,
 * immune to network jitter and cell-tick phase drift.
 */
export function updateEntityFromServer(
  entities: Map<number, ClientEntity>,
  serverState: AnyEntity,
  producedAtMs: number,
  audit?: ReplicationAudit,
  nowMs?: number,
  allowStreamReset = false,
): boolean {
  const id = serverState.netID;
  const existing = entities.get(id);

  if (!existing) {
    if (audit && nowMs !== undefined) recordEntityCreate(audit, nowMs, id);
    const buffer = newBuffer();
    const first = sampleFrom(serverState, producedAtMs, 0);
    buffer.push(first);
    const ent: ClientEntity = {
      ...serverState,
      prevX: serverState.worldX,
      prevY: serverState.worldY,
      isReplica: false,
      isGhost: false,
      buffer,
      renderX: first.worldX,
      renderY: first.worldY,
      renderRot: first.rotation,
    };
    entities.set(id, ent);
    return true;
  }
  // Skip frames older than the newest sample we already hold. At a cell
  // boundary the same netID is delivered from two cell authorities; the
  // ex-authority's final in-flight frame can arrive last. The position
  // buffer drops it (push), but Object.assign below would still snap
  // non-interpolated fields (radius/size, …) backward to the stale value —
  // flickering — so gate the whole snapshot on the same rule.
  if (existing.buffer.isStale(producedAtMs, serverState.authorityEpoch)) {
    if (!allowStreamReset) return false;
    // The enclosing stream generation supersedes authority history from the
    // previous stream. In particular, rollback may resume an older entity
    // epoch on a newer stream after the speculative destination was visible.
    existing.buffer.reset();
  }
  const prevRot = existing.renderRot;
  Object.assign(existing, serverState);
  existing.prevX = existing.renderX;
  existing.prevY = existing.renderY;
  existing.buffer.push(sampleFrom(serverState, producedAtMs, prevRot));
  return true;
}

/**
 * interpolateEntities sets renderX/Y/Rot on every entity by
 * interpolating between the two buffer samples that bracket the adaptive
 * connection-level playback cursor. Packet loss and phase drift adjust that
 * shared cursor; extrapolation past the newest sample remains capped.
 */
export function interpolateEntities(
  entities: Map<number, ClientEntity>,
  playback: AdaptivePlaybackController,
  clientNowMs: number,
): void {
  const renderTime = playback.renderTime(clientNowMs);
  if (renderTime === null) return;
  for (const ent of entities.values()) {
    const r = ent.buffer.sampleAt(renderTime, playback.currentDelayMs);
    if (r) {
      ent.renderX = r.renderX;
      ent.renderY = r.renderY;
      ent.renderRot = r.renderRot;
    }
  }
}

export { lerp, lerpAngle };
