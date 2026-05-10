export type Point = { x: number; y: number };

export type ScaleOpts = {
  min?: number; // clamp the scale's lower bound (default: data min)
  max?: number; // clamp the scale's upper bound (default: data max)
};

// scaleSeries maps a numeric series onto canvas coordinates. x is spread
// evenly across [0, width]; y maps [scaleMin, scaleMax] to [height, 0]
// (canvas y grows downward, so larger values get lower y).
export function scaleSeries(
  values: readonly number[],
  width: number,
  height: number,
  opts: ScaleOpts = {},
): Point[] {
  const n = values.length;
  if (n === 0) return [];
  const lo = opts.min ?? Math.min(...values);
  const hi = opts.max ?? Math.max(...values);
  if (hi === lo) {
    // Flat: render a midline.
    return values.map((_, i) => ({
      x: n === 1 ? width / 2 : (i * width) / (n - 1),
      y: height / 2,
    }));
  }
  const dx = n === 1 ? width / 2 : width / (n - 1);
  return values.map((v, i) => ({
    x: n === 1 ? width / 2 : i * dx,
    y: height - ((v - lo) / (hi - lo)) * height,
  }));
}
