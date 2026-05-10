import { apiGet, apiPost, ApiError } from "./api";
import type { AuthSession } from "./types";

export const auth = {
  async login(username: string, password: string): Promise<AuthSession> {
    return apiPost<AuthSession>("/admin/api/auth/login", { username, password });
  },

  async logout(): Promise<void> {
    try {
      await apiPost<{ ok: boolean }>("/admin/api/auth/logout");
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") return;
      throw e;
    }
  },

  // session() returns null when no valid session cookie is present, the
  // session itself when one exists. Used at app boot to decide login vs cluster.
  async session(): Promise<AuthSession | null> {
    try {
      return await apiGet<AuthSession>("/admin/api/auth/session");
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") return null;
      throw e;
    }
  },
};
