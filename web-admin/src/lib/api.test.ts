import { describe, it, expect, beforeEach, vi } from "vitest";
import { apiGet, apiPost, ApiError } from "./api";

describe("api", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("apiGet parses JSON response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    const res = await apiGet<{ ok: boolean }>("/admin/api/cluster");
    expect(res.ok).toBe(true);
  });

  it("apiGet throws ApiError on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: "no session" }), {
          status: 401,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    await expect(apiGet("/admin/api/cluster")).rejects.toMatchObject({
      kind: "rbac",
      status: 401,
    });
  });

  it("apiPost includes body and content-type", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(JSON.stringify({ ok: true }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await apiPost("/admin/api/commands/cell.split", { CellID: "cell_0_0" });
    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;
    expect(calls.length).toBeGreaterThan(0);
    const init = calls[0]?.[1];
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>)?.["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init?.body as string)).toEqual({ CellID: "cell_0_0" });
  });

  it("network failure surfaces as kind=network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    await expect(apiGet("/admin/api/cluster")).rejects.toMatchObject({
      kind: "network",
    });
  });
});
