import type { SpaceClient } from "../../sdk/client.js";
import { authLogin, authRegister, authValidateToken, TOKEN_KEY } from "../auth.js";

export interface LoginResult {
  userId: string;
  username: string;
  sessionToken: string;
}

/**
 * setupLogin returns a Promise that resolves with LoginResult once auth
 * succeeds. The caller then sets state.playerUsername and proceeds with the
 * spawn flow. If a saved session token exists it is validated first —
 * on success the form is skipped entirely.
 */
export async function setupLogin(client: SpaceClient): Promise<LoginResult> {
  const overlay = document.getElementById("login-overlay")!;
  const usernameEl = document.getElementById(
    "login-username",
  ) as HTMLInputElement;
  const passwordEl = document.getElementById(
    "login-password",
  ) as HTMLInputElement;
  const submitBtn = document.getElementById(
    "login-submit",
  ) as HTMLButtonElement;
  const registerBtn = document.getElementById(
    "login-register-toggle",
  ) as HTMLButtonElement;
  const hint = document.getElementById("login-hint")!;

  overlay.style.display = "flex";

  // Try saved token first.
  const stored = localStorage.getItem(TOKEN_KEY);
  if (stored) {
    try {
      const resp = await authValidateToken(client, stored);
      overlay.style.display = "none";
      return {
        userId: resp.userId,
        username: resp.username,
        sessionToken: stored,
      };
    } catch {
      localStorage.removeItem(TOKEN_KEY);
      // fall through to form
    }
  }

  // Restore last-used username.
  usernameEl.value = (localStorage.getItem("username") || "").toLowerCase();
  usernameEl.focus();

  return new Promise((resolve) => {
    let mode: "login" | "register" = "login";

    function setHint(msg: string, cls: string) {
      hint.textContent = msg;
      hint.className = "hint" + (cls ? " " + cls : "");
    }

    async function submit() {
      const username = usernameEl.value.trim().toLowerCase();
      const password = passwordEl.value;
      if (!username || !password) {
        setHint("Username and password required", "error");
        return;
      }
      setHint(mode === "login" ? "Logging in..." : "Registering...", "");
      submitBtn.disabled = true;
      registerBtn.disabled = true;
      try {
        const resp =
          mode === "login"
            ? await authLogin(client, username, password)
            : await authRegister(client, username, password);
        localStorage.setItem(TOKEN_KEY, resp.sessionToken);
        localStorage.setItem("username", resp.username);
        overlay.style.display = "none";
        resolve({
          userId: resp.userId,
          username: resp.username,
          sessionToken: resp.sessionToken,
        });
      } catch (e: unknown) {
        const msg = e instanceof Error ? e.message : String(e);
        setHint(msg.slice(0, 80), "error");
      } finally {
        submitBtn.disabled = false;
        registerBtn.disabled = false;
      }
    }

    submitBtn.addEventListener("click", () => {
      mode = "login";
      submit();
    });

    registerBtn.addEventListener("click", () => {
      if (mode !== "register") {
        mode = "register";
        setHint("Pick a unique callsign + password (8+ chars)", "");
        registerBtn.textContent = "Submit";
        submitBtn.style.display = "none";
        return;
      }
      submit();
    });

    function onKey(e: KeyboardEvent) {
      e.stopPropagation();
      if (e.key === "Enter") {
        submit();
      }
    }
    usernameEl.addEventListener("keydown", onKey);
    passwordEl.addEventListener("keydown", onKey);
  });
}

export function showLogin(error?: string): void {
  const overlay = document.getElementById("login-overlay")!;
  const hint = document.getElementById("login-hint")!;
  overlay.style.display = "flex";
  if (error) {
    hint.textContent = error;
    hint.className = "hint error";
  }
  const usernameEl = document.getElementById(
    "login-username",
  ) as HTMLInputElement | null;
  usernameEl?.focus();
}

export function clearStoredToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}
