// Typed glue for the /tunables page. Talks to the tune.list / tune.set cmdsys
// verbs via the same /admin/api/commands/<verb> envelope every other route uses
// (see users.svelte, world-store.svelte.ts). The command-invoke response shape
// (pkg/admin/api_commands.go::handleCommandInvoke) is:
//
//   { ok: true, result?: <typed result>, targets?: [...], traceId }
//
// All tune verbs are RouteAllHosts (the per-host tunable registries are only
// populated on cell-bearing hosts, so reads must fan out too). A single host
// collapses to `result`; with multiple hosts the per-host results appear in
// `targets[]`. The tune verbs all return a `tuneResult{ Rows []tuneRow }`,
// and tune.set additionally publishes the post-mutation rows on the "tunables"
// SSE topic — so the page relies on SSE for the authoritative live state and
// only uses tune.set's direct response opportunistically.

import { apiPost } from "$lib/api";

// Mirrors pkg/mmokit/tunable_verbs.go::tuneRow. All values are strings; empty
// Min/Max mean "unbounded". Kind ∈ "int" | "uint" | "float" | "bool".
export type TuneRow = {
  System: string;
  Field: string;
  Value: string;
  Default: string;
  Min: string;
  Max: string;
  Kind: string;
};

export type TuneSystem = { name: string; fields: TuneRow[] };

type TuneResult = { Rows?: TuneRow[] | null };

// invokeResp envelope from handleCommandInvoke.
type InvokeResp = {
  ok: boolean;
  result?: TuneResult;
  // Inner elements are raw cmdsys.TargetResult (pkg/cmdsys/command.go) — no JSON
  // tags, so the keys serialize capitalized (OK / Result).
  targets?: { OK?: boolean; Result?: TuneResult }[];
  error?: string;
};

// rowsFromResp extracts the flat tuneRow list from a command response,
// tolerating both the single-target (`result`) and multi-target (`targets[]`)
// shapes. Multi-target rows are merged and de-duplicated by System+Field.
export function rowsFromResp(resp: InvokeResp | undefined): TuneRow[] {
  if (!resp) return [];
  if (resp.result?.Rows) return resp.result.Rows;
  if (resp.targets && resp.targets.length > 0) {
    const seen = new Set<string>();
    const out: TuneRow[] = [];
    for (const t of resp.targets) {
      for (const r of t.Result?.Rows ?? []) {
        const key = `${r.System}::${r.Field}`;
        if (seen.has(key)) continue;
        seen.add(key);
        out.push(r);
      }
    }
    return out;
  }
  return [];
}

// groupRows buckets flat rows by System with a stable sort: system asc, field
// asc. The backend already sorts fields, but we re-sort defensively so SSE
// updates and the initial fetch render identically.
export function groupRows(rows: TuneRow[]): TuneSystem[] {
  const bySystem = new Map<string, TuneRow[]>();
  for (const r of rows) {
    let arr = bySystem.get(r.System);
    if (!arr) {
      arr = [];
      bySystem.set(r.System, arr);
    }
    arr.push(r);
  }
  const systems: TuneSystem[] = [];
  for (const [name, fields] of bySystem) {
    fields.sort((a, b) => a.Field.localeCompare(b.Field));
    systems.push({ name, fields });
  }
  systems.sort((a, b) => a.name.localeCompare(b.name));
  return systems;
}

export async function fetchTunables(): Promise<TuneSystem[]> {
  const resp = await apiPost<InvokeResp>("/admin/api/commands/tune.list", {});
  return groupRows(rowsFromResp(resp));
}

export async function setTunable(
  system: string,
  field: string,
  value: string,
): Promise<void> {
  await apiPost<InvokeResp>("/admin/api/commands/tune.set", {
    System: system,
    Field: field,
    Value: value,
  });
}

// resetTunable resets one field (when `field` is given) or every field in a
// system (when omitted) to its tag default via the tune.reset verb. Like
// tune.set, the verb publishes the post-reset rows on the "tunables" SSE topic,
// so the page picks up the authoritative values from the stream.
export async function resetTunable(
  system: string,
  field?: string,
): Promise<void> {
  const args: { System: string; Field?: string } = { System: system };
  if (field) args.Field = field;
  await apiPost<InvokeResp>("/admin/api/commands/tune.reset", args);
}
