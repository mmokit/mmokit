/** Viewport scale factor: world units mapped to the smaller canvas dimension. */
export const VIEWPORT_SCALE = 3500;

// Snapshot interpolation — render-frame smoothing of server snapshots.

/**
 * Number of samples retained per entity in the interpolation ring. Must
 * be large enough to cover RENDER_DELAY with margin for phase jitter at
 * cell handoff. 4 gives ~200ms window at 20Hz — one tick of margin
 * behind renderTime even under a dropped-frame burst.
 */
export const RING_SIZE = 8;

/**
 * How far behind the latest server snapshot the client renders, in
 * milliseconds. Two ticks at 20Hz absorbs one dropped frame plus
 * typical cell-goroutine phase jitter.
 */
export const RENDER_DELAY = 100;

/** Adaptive playback ceiling under burst jitter/loss. */
export const MAX_RENDER_DELAY = 300;

/**
 * Maximum forward velocity extrapolation when render time runs past
 * the newest sample. Capped at one tick so extrapolation stays bounded.
 */
export const MAX_EXTRAPOLATE_MS = 50;

/**
 * EMA smoothing factor for the clock-offset observation loop. 0.1
 * converges in ~20 observations (one second at 20Hz) while rejecting
 * spikes from one-off network jitter.
 */
export const CLOCK_SYNC_ALPHA = 0.1;
