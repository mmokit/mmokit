export type Bar = { value: number; width: number };

// layoutBars returns each value's pixel width scaled so the max value
// fills `maxWidth`. Returns 0 widths when every value is zero.
export function layoutBars(values: readonly number[], maxWidth: number): Bar[] {
  if (values.length === 0) return [];
  const max = values.reduce((a, b) => (b > a ? b : a), 0);
  if (max === 0) return values.map((v) => ({ value: v, width: 0 }));
  return values.map((v) => ({ value: v, width: (v / max) * maxWidth }));
}
