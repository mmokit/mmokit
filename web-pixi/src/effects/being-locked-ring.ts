import { Container, Graphics, Text } from "pixi.js";
import type { ShipEntity, AsteroidEntity, NPCEntity } from "../../sdk/index.js";
import { px } from "../view";
import type { GameState } from "../state";
import type { ClientEntity } from "../types";
import { getShip } from "../entity-accessors";

const COLOR_WARNING = 0xff4444;
const COLOR_LOCKING = 0xff6600;
const COLOR_LOCKED = 0xff0000;

// Entity shapes that may carry LockedBy scalar fields via replication.
type LockableEntity = ShipEntity | AsteroidEntity | NPCEntity;

interface RingEntry {
	container: Container;
	ring: Graphics;
	label: Text;
}

/**
 * BeingLockedRing renders a warning ring around any entity currently being
 * target-locked. The lock source is the replicated `lockerNetID` /
 * `lockerProgress` fields on each lockable entity (not PlayerOwnStateMsg).
 * Zero lockerNetID means nobody is locking this entity.
 */
export class BeingLockedRing {
	private parent: Container;
	private entries = new Map<number, RingEntry>();

	constructor(parent: Container) {
		this.parent = parent;
	}

	update(state: GameState, now: number): void {
		const alive = new Set<number>();

		for (const [netID, ent] of state.entities) {
			const lb = extractLockedBy(ent);
			if (!lb || lb.lockerNetID === 0) continue;

			alive.add(netID);
			let entry = this.entries.get(netID);
			if (!entry) {
				entry = createRingEntry(this.parent);
				this.entries.set(netID, entry);
			}
			drawRing(entry, ent, lb, state, now);
		}

		// Clean up rings for entities that are no longer locked.
		for (const [netID, entry] of this.entries) {
			if (!alive.has(netID)) {
				this.parent.removeChild(entry.container);
				entry.container.destroy({ children: true });
				this.entries.delete(netID);
			}
		}
	}
}

function extractLockedBy(ent: ClientEntity): { lockerNetID: number; lockerProgress: number } | null {
	// Field names match those emitted by the generator from LockerNetID / LockerProgress.
	// If the SDK generator changed casing (e.g. `lockerNetId`), update both fields here.
	const e = ent.current as Partial<LockableEntity> & { lockerNetID?: number; lockerProgress?: number };
	if (typeof e.lockerNetID !== "number") return null;
	return { lockerNetID: e.lockerNetID, lockerProgress: e.lockerProgress ?? 0 };
}

function createRingEntry(parent: Container): RingEntry {
	const container = new Container();
	parent.addChild(container);

	const ring = new Graphics();
	container.addChild(ring);

	const label = new Text({
		text: "",
		style: { fontFamily: "monospace", fontSize: 11, fill: COLOR_WARNING },
	});
	label.anchor.set(0.5, 1);
	label.scale.set(px(1), px(1));
	container.addChild(label);

	return { container, ring, label };
}

function drawRing(
	entry: RingEntry,
	ent: ClientEntity,
	lb: { lockerNetID: number; lockerProgress: number },
	state: GameState,
	now: number,
): void {
	entry.container.position.set(ent.renderX, ent.renderY);

	const asShip = ent.current as Partial<ShipEntity>;
	const w = asShip.width ?? 1;
	const h = asShip.height ?? 1;
	const baseRadius = Math.max(w, h, 1) * 0.5 + px(18);
	const progress = lb.lockerProgress;
	const locked = progress >= 1.0;

	// Resolve locker name for display (best effort — locker may be outside AoI).
	const locker = state.entities.get(lb.lockerNetID);
	const lockerName = (locker ? getShip(locker)?.name : undefined) || "???";

	entry.ring.clear();

	if (locked) {
		const pulse = 0.6 + 0.4 * Math.sin(now * 0.006);
		entry.ring
			.circle(0, 0, baseRadius)
			.stroke({ color: COLOR_LOCKED, width: px(3), alpha: pulse });
		entry.label.text = `LOCKED BY ${lockerName.toUpperCase()}`;
		entry.label.style.fill = COLOR_LOCKED;
	} else {
		drawDashedCircle(entry.ring, baseRadius, 0x333333, px(1.5), 0.4, now);

		const color = progress > 0.5 ? COLOR_WARNING : COLOR_LOCKING;
		const startAngle = -Math.PI / 2;
		const endAngle = startAngle + progress * Math.PI * 2;
		const pulse = 0.6 + 0.4 * Math.sin(now * 0.008);
		const sx = Math.cos(startAngle) * baseRadius;
		const sy = Math.sin(startAngle) * baseRadius;
		entry.ring
			.moveTo(sx, sy)
			.arc(0, 0, baseRadius, startAngle, endAngle)
			.stroke({ color, width: px(3), alpha: pulse });

		entry.label.text = `LOCKING: ${lockerName.toUpperCase()} ${Math.floor(progress * 100)}%`;
		entry.label.style.fill = color;
	}

	const hw = w * 0.5;
	const hh = h * 0.5;
	const halfDiag = Math.sqrt(hw * hw + hh * hh);
	entry.label.position.set(0, -(halfDiag + px(52)));
}

function drawDashedCircle(
	ring: Graphics,
	radius: number,
	color: number,
	width: number,
	alpha: number,
	now: number,
): void {
	const dashCount = 24;
	const gapFraction = 0.4;
	const segmentAngle = (Math.PI * 2) / dashCount;
	const dashAngle = segmentAngle * (1 - gapFraction);
	const rotation = now * 0.001;

	for (let i = 0; i < dashCount; i++) {
		const startAngle = rotation + i * segmentAngle;
		const endAngle = startAngle + dashAngle;
		const x0 = Math.cos(startAngle) * radius;
		const y0 = Math.sin(startAngle) * radius;
		ring.moveTo(x0, y0);

		const steps = 3;
		for (let s = 1; s <= steps; s++) {
			const a = startAngle + (endAngle - startAngle) * (s / steps);
			ring.lineTo(Math.cos(a) * radius, Math.sin(a) * radius);
		}
		ring.stroke({ color, width, alpha });
	}
}
