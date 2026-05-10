export type SortDir = "asc" | "desc";

// sortRows returns a new array sorted by the given accessor. Numbers and
// strings sort naturally; undefined/null values are pushed to the end
// regardless of direction.
export function sortRows<T>(
  rows: readonly T[],
  accessor: (r: T) => string | number | undefined | null,
  dir: SortDir,
): T[] {
  const out = rows.slice();
  out.sort((a, b) => {
    const av = accessor(a);
    const bv = accessor(b);
    if (av == null && bv == null) return 0;
    if (av == null) return 1;
    if (bv == null) return -1;
    if (av < bv) return dir === "asc" ? -1 : 1;
    if (av > bv) return dir === "asc" ? 1 : -1;
    return 0;
  });
  return out;
}
