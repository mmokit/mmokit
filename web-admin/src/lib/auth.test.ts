import { describe, it, expect, beforeEach, vi } from "vitest";
import { auth } from "./auth";

describe("auth", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("login posts credentials and returns session", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(
        JSON.stringify({ user: "josh", grants: ["*.*"], expiresAt: "2099-01-01T00:00:00Z" }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const sess = await auth.login("josh", "secret");
    expect(sess.user).toBe("josh");
    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;
    expect(JSON.parse(calls[0][1]?.body as string)).toEqual({ username: "josh", password: "secret" });
  });

  it("session() returns null on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: "no session" }), {
          status: 401,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    expect(await auth.session()).toBeNull();
  });
});
