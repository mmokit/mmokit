import type { GameState, SnakeData, SegmentPos } from "./state";

let TICK_MS = 50;
let TICK_S = TICK_MS / 1000;

export function setTickRate(rate: number): void {
  TICK_MS = 1000 / rate;
  TICK_S = TICK_MS / 1000;
}

export interface InterpolatedSnake {
  id: number;
  headX: number;
  headY: number;
  angle: number;
  speed: number;
  mass: number;
  skinID: number;
  boosting: boolean;
  name: string;
  length: number;
  segments: SegmentPos[];
}

export function getInterpolationT(state: GameState): number {
  if (state.lastTickTime === 0) return 1;
  const elapsed = performance.now() - state.lastTickTime;
  return Math.min(elapsed / TICK_MS, 2.0);
}

function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

/**
 * Smoothly interpolate a snake using dead-reckoning + correction.
 *
 * Instead of pure lerp between prev/curr tick snapshots (which stutters at
 * tick boundaries), we:
 * 1. Compute a target position by interpolating/extrapolating from tick data
 * 2. Dead-reckon the head forward using angle + speed each frame
 * 3. Blend dead-reckoning with the authoritative target
 * 4. Smooth segments with exponential decay toward their targets
 */
export function interpolateSnake(
  snake: SnakeData,
  t: number,
  dt: number
): InterpolatedSnake {
  // Detect large position jumps (cross-cell transfer) and snap
  const jumpDx = snake.currHeadX - snake.prevHeadX;
  const jumpDy = snake.currHeadY - snake.prevHeadY;
  const snap = jumpDx * jumpDx + jumpDy * jumpDy > 500 * 500;

  // --- Head position ---
  let targetX: number;
  let targetY: number;

  if (snap) {
    targetX = snake.currHeadX;
    targetY = snake.currHeadY;
  } else if (t <= 1) {
    // Normal interpolation between prev and curr tick
    targetX = lerp(snake.prevHeadX, snake.currHeadX, t);
    targetY = lerp(snake.prevHeadY, snake.currHeadY, t);
  } else {
    // Extrapolate past current tick using velocity
    const extra = (t - 1) * TICK_S;
    targetX = snake.currHeadX + Math.cos(snake.angle) * snake.speed * extra;
    targetY = snake.currHeadY + Math.sin(snake.angle) * snake.speed * extra;
  }

  // Smooth renderHead toward target (exponential decay)
  // Higher factor = snappier, lower = smoother. 15-20 is a good sweet spot.
  const smoothFactor = 1 - Math.exp(-18 * dt);
  if (snap) {
    snake.renderHeadX = targetX;
    snake.renderHeadY = targetY;
  } else {
    snake.renderHeadX += (targetX - snake.renderHeadX) * smoothFactor;
    snake.renderHeadY += (targetY - snake.renderHeadY) * smoothFactor;
  }

  const headX = snake.renderHeadX;
  const headY = snake.renderHeadY;

  // --- Segments ---
  const prevSegs = snake.prevSegments;
  const currSegs = snake.currSegments;
  const segCount = currSegs.length;
  const segments: SegmentPos[] = [];

  // Initialize or resize render segments gracefully
  if (!snake.renderSegments) {
    snake.renderSegments = [];
    for (let i = 0; i < segCount; i++) {
      snake.renderSegments.push({ x: currSegs[i].x, y: currSegs[i].y });
    }
  } else if (snake.renderSegments.length < segCount) {
    // Grow: initialize new segments at the tail position (last existing segment)
    const tail = snake.renderSegments[snake.renderSegments.length - 1];
    while (snake.renderSegments.length < segCount) {
      snake.renderSegments.push({ x: tail.x, y: tail.y });
    }
  } else if (snake.renderSegments.length > segCount) {
    // Shrink: just truncate
    snake.renderSegments.length = segCount;
  }

  // Smooth factor for segments — slightly softer than head for a trailing feel
  const segSmooth = 1 - Math.exp(-14 * dt);

  for (let i = 0; i < segCount; i++) {
    let tx: number, ty: number;
    if (snap || i >= prevSegs.length) {
      tx = currSegs[i].x;
      ty = currSegs[i].y;
    } else if (t <= 1) {
      tx = lerp(prevSegs[i].x, currSegs[i].x, t);
      ty = lerp(prevSegs[i].y, currSegs[i].y, t);
    } else {
      // Extrapolate segments by shifting in the direction of their predecessor
      tx = currSegs[i].x;
      ty = currSegs[i].y;
    }

    if (snap) {
      snake.renderSegments[i].x = tx;
      snake.renderSegments[i].y = ty;
    } else {
      snake.renderSegments[i].x += (tx - snake.renderSegments[i].x) * segSmooth;
      snake.renderSegments[i].y += (ty - snake.renderSegments[i].y) * segSmooth;
    }

    segments.push({
      x: snake.renderSegments[i].x,
      y: snake.renderSegments[i].y,
    });
  }

  return {
    id: snake.id,
    headX,
    headY,
    angle: snake.angle,
    speed: snake.speed,
    mass: snake.mass,
    skinID: snake.skinID,
    boosting: snake.boosting,
    name: snake.name,
    length: snake.length,
    segments,
  };
}
