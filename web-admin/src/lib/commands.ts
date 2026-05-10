// fuzzyScore returns a positive number when every character of `query`
// appears in `text` in order (case-insensitive), 0 otherwise. Score
// favors earlier matches and contiguous runs so prefix matches outrank
// scattered hits — good enough for the entity palette without a fuzzy lib.
export function fuzzyScore(text: string, query: string): number {
  if (!query) return 1; // empty query matches everything weakly
  const t = text.toLowerCase();
  const q = query.toLowerCase();
  let score = 0;
  let textIdx = 0;
  let prevMatch = -2; // index of last matched char in text
  for (let qi = 0; qi < q.length; qi++) {
    const ch = q[qi];
    let found = -1;
    for (let ti = textIdx; ti < t.length; ti++) {
      if (t[ti] === ch) {
        found = ti;
        break;
      }
    }
    if (found < 0) return 0;
    // Higher score when match is at position 0, contiguous with the
    // previous match, or near the start of the string.
    score += 100 - found; // earlier matches → higher
    if (found === prevMatch + 1) score += 50; // contiguous bonus
    prevMatch = found;
    textIdx = found + 1;
  }
  return score;
}
