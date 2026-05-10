// Centralized fetch wrappers. Every API call goes through here so error
// shapes are uniform and panels can render consistent toasts/banners.

export type ApiErrorKind =
  | "http"        // generic non-2xx that doesn't fit a more specific kind
  | "network"     // DNS / TLS / dropped — fetch threw
  | "rbac"        // 401 / 403
  | "validation"  // 400 with field errors
  | "cmdsys";     // 409 — cmdsys returned a domain error

export class ApiError extends Error {
  kind: ApiErrorKind;
  status: number;
  fieldErrors?: Record<string, string>;
  raw?: unknown;

  constructor(kind: ApiErrorKind, status: number, message: string, raw?: unknown) {
    super(message);
    this.kind = kind;
    this.status = status;
    this.raw = raw;
  }
}

const COMMON_HEADERS: HeadersInit = {
  "Content-Type": "application/json",
};

async function call<T>(method: string, url: string, body?: unknown): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(url, {
      method,
      credentials: "same-origin",
      headers: body !== undefined ? COMMON_HEADERS : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw new ApiError("network", 0, (e as Error).message ?? "network error");
  }

  let payload: unknown;
  const ct = resp.headers.get("content-type") ?? "";
  if (ct.includes("application/json")) {
    try {
      payload = await resp.json();
    } catch {
      payload = undefined;
    }
  }

  if (!resp.ok) {
    const msg =
      (payload as { error?: string } | undefined)?.error ?? `HTTP ${resp.status}`;
    const kind: ApiErrorKind =
      resp.status === 401 || resp.status === 403
        ? "rbac"
        : resp.status === 400
          ? "validation"
          : resp.status === 409
            ? "cmdsys"
            : "http";
    throw new ApiError(kind, resp.status, msg, payload);
  }

  return (payload as T) ?? (undefined as T);
}

export function apiGet<T>(url: string): Promise<T> {
  return call<T>("GET", url);
}

export function apiPost<T = unknown>(url: string, body?: unknown): Promise<T> {
  return call<T>("POST", url, body ?? {});
}

export function apiDelete<T = unknown>(url: string): Promise<T> {
  return call<T>("DELETE", url);
}
