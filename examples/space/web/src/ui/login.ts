import { authLogin, authMe, authRegister } from "../auth.js";

export interface LoginResult {
  userId: string;
  username: string;
}

/**
 * setupLogin returns a Promise that resolves with LoginResult once
 * auth completes. Tries cookie-based resume via /auth/me first; on
 * 401 falls through to the credential form.
 */
export async function setupLogin(): Promise<LoginResult> {
  const overlay = document.getElementById("login-overlay")!;
  const spinner = document.getElementById("login-spinner")!;
  const panel = document.getElementById("login-panel")!;
  const usernameEl = document.getElementById("login-username") as HTMLInputElement;
  const passwordEl = document.getElementById("login-password") as HTMLInputElement;
  const submitBtn = document.getElementById("login-submit") as HTMLButtonElement;
  const registerBtn = document.getElementById("login-register-toggle") as HTMLButtonElement;
  const hint = document.getElementById("login-hint")!;

  overlay.style.display = "flex";

  // Try cookie-based resume first.
  spinner.style.display = "flex";
  panel.style.display = "none";
  try {
    const me = await authMe();
    if (me) {
      overlay.style.display = "none";
      spinner.style.display = "none";
      return { userId: me.userId, username: me.username };
    }
  } catch (e) {
    // Network error or 5xx — fall through to form. The user can
    // retry; if the cookie is bad, the next login attempt issues a
    // fresh one.
    console.warn("authMe failed:", e);
  }
  spinner.style.display = "none";
  panel.style.display = "block";

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
        const me = mode === "login"
          ? await authLogin(username, password)
          : await authRegister(username, password);
        localStorage.setItem("username", me.username);
        overlay.style.display = "none";
        resolve({ userId: me.userId, username: me.username });
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
  const usernameEl = document.getElementById("login-username") as HTMLInputElement | null;
  usernameEl?.focus();
}
