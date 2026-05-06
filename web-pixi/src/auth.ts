// HTTPS-based auth. Replaces the previous op-channel + localStorage
// pattern. The session cookie is HttpOnly and managed by the browser;
// JS never sees the token.

export interface MeResponse {
  userId: string;
  username: string;
  expiresAtMs: number;
}

export interface AuthError extends Error {
  code: number; // pkg/auth.AuthError* constant value
  retryAfterMs?: number;
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw await parseError(res);
  }
  if (res.status === 200 && res.headers.get("Content-Length") !== "0") {
    return (await res.json()) as T;
  }
  return {} as T;
}

async function parseError(res: Response): Promise<AuthError> {
  let body: { code?: number; message?: string; retry_after_ms?: number } = {};
  try {
    body = await res.json();
  } catch {
    /* non-JSON response */
  }
  const e = new Error(body.message || `HTTP ${res.status}`) as AuthError;
  e.code = body.code ?? 0;
  if (body.retry_after_ms) e.retryAfterMs = body.retry_after_ms;
  return e;
}

export function authLogin(username: string, password: string): Promise<MeResponse> {
  return postJSON<MeResponse>("/auth/login", { username, password });
}

export function authRegister(username: string, password: string, email?: string): Promise<MeResponse> {
  return postJSON<MeResponse>("/auth/register", { username, password, email });
}

export function authLogout(): Promise<void> {
  return postJSON<void>("/auth/logout", {});
}

export async function authMe(): Promise<MeResponse | null> {
  const res = await fetch("/auth/me", { credentials: "same-origin" });
  if (res.status === 401) return null;
  if (!res.ok) throw await parseError(res);
  return (await res.json()) as MeResponse;
}

export async function authChangePassword(current: string, next: string): Promise<void> {
  await postJSON<void>("/auth/change-password", {
    current_password: current,
    new_password: next,
  });
}
