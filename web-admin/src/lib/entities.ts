// Typed glue for the /entities page. Calls the entity.list cmdsys verb via the
// /admin/api/commands/<verb> envelope. entity.list is RouteAllHosts, so a single
// host collapses to `result` while multiple hosts appear in `targets[]`.
import { apiPost } from "$lib/api";
import type { EntityListRow } from "$lib/types";

type ListResult = { Entities?: EntityListRow[] | null };

type InvokeResp = {
  ok: boolean;
  result?: ListResult;
  targets?: { OK?: boolean; Result?: ListResult }[];
  error?: string;
};

// rowsFromResp flattens the entity.list response across single/multi-target
// shapes, de-duplicating by NetID.
function rowsFromResp(resp: InvokeResp | undefined): EntityListRow[] {
  if (!resp) return [];
  if (resp.result?.Entities) return resp.result.Entities;
  if (resp.targets && resp.targets.length > 0) {
    const seen = new Set<number>();
    const out: EntityListRow[] = [];
    for (const t of resp.targets) {
      for (const r of t.Result?.Entities ?? []) {
        if (seen.has(r.NetID)) continue;
        seen.add(r.NetID);
        out.push(r);
      }
    }
    return out;
  }
  return [];
}

// listEntities fetches the live entity list, optionally filtered by kind.
export async function listEntities(kind?: string): Promise<EntityListRow[]> {
  const resp = await apiPost<InvokeResp>(
    "/admin/api/commands/entity.list",
    kind ? { Kind: kind } : {},
  );
  return rowsFromResp(resp);
}
