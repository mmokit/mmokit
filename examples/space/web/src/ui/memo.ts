/** Simple string-hash memoization for skipping unnecessary DOM rebuilds. */
const hashes = new Map<string, string>();

/**
 * Returns true if the data has changed since the last call with the same key.
 * Pass the values that drive a DOM rebuild as `parts` — if they haven't changed,
 * returns false so you can skip the rebuild.
 */
export function needsRebuild(key: string, ...parts: unknown[]): boolean {
  const hash = parts.map(p =>
    p instanceof Map ? JSON.stringify([...p.entries()]) :
    Array.isArray(p) ? JSON.stringify(p) :
    String(p)
  ).join("|");
  if (hashes.get(key) === hash) return false;
  hashes.set(key, hash);
  return true;
}

/** Force a key to rebuild on next check. */
export function invalidate(key: string): void {
  hashes.delete(key);
}
