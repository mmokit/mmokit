// Name-only auth flow for the 4node demo. The auth service requires a
// password; we use a fixed dev password so the UI can stay single-field.
// Order: /auth/me (cookie resume) → /auth/register (first-time) →
// /auth/login (account already exists). On 401, /auth/me clears the
// stale cookie server-side, so a bad cookie self-heals on next click.

const DEV_PASSWORD = "4node-demo-password";

interface MeResponse {
  user_id: string;
  username: string;
  expires_at_ms: number;
}

async function postJSON(path: string, body: unknown): Promise<Response> {
  return fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

async function authMe(): Promise<MeResponse | null> {
  const res = await fetch("/auth/me", { credentials: "same-origin" });
  if (res.status === 401) return null;
  if (!res.ok) throw new Error(`/auth/me: HTTP ${res.status}`);
  return (await res.json()) as MeResponse;
}

// loginAsName ensures the browser holds a valid auth cookie for `name`,
// auto-creating the account on first use. Returns the resolved username
// (lowercased by the server).
export async function loginAsName(name: string): Promise<string> {
  const username = name.trim().toLowerCase();
  if (!username) throw new Error("name required");

  // 1. Cookie resume — if it's still valid AND for the same name, skip
  //    the round-trip. If it's for a different name, fall through to
  //    register/login so the user can switch accounts.
  const me = await authMe();
  if (me && me.username === username) return me.username;

  // 2. Try registering. On 200 we're authed and have a fresh cookie.
  const reg = await postJSON("/auth/register", { username, password: DEV_PASSWORD });
  if (reg.ok) return username;

  // 3. Username taken (409) → log in instead.
  if (reg.status === 409) {
    const login = await postJSON("/auth/login", { username, password: DEV_PASSWORD });
    if (login.ok) return username;
    const body = await login.json().catch(() => ({ message: `HTTP ${login.status}` }));
    throw new Error(`login failed: ${body.message ?? login.status}`);
  }

  const body = await reg.json().catch(() => ({ message: `HTTP ${reg.status}` }));
  throw new Error(`register failed: ${body.message ?? reg.status}`);
}
