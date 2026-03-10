export function setupLogin(onLogin: (username: string) => void): void {
  const overlay = document.getElementById("login-overlay")!;
  const input = document.getElementById("login-input") as HTMLInputElement;
  const hint = document.getElementById("login-hint")!;

  // Restore saved username
  const saved = localStorage.getItem("username") || "";
  input.value = saved;
  input.focus();
  updateHint();

  input.addEventListener("input", updateHint);

  input.addEventListener("keydown", (e) => {
    // Stop all login keypresses from reaching the game input handler
    e.stopPropagation();

    if (e.key === "Enter") {
      const username = input.value.trim().toLowerCase();
      if (username.length > 0) {
        localStorage.setItem("username", username);
        overlay.style.display = "none";
        onLogin(username);
      }
    }
    // Force lowercase
    setTimeout(() => {
      input.value = input.value.toLowerCase();
    }, 0);
  });

  function updateHint() {
    const val = input.value.trim();
    if (val.length > 0) {
      hint.textContent = "Press ENTER to launch";
      hint.className = "hint ready";
    } else {
      hint.textContent = "Type a username to begin";
      hint.className = "hint";
    }
  }
}

export function showLogin(error?: string): void {
  const overlay = document.getElementById("login-overlay")!;
  const hint = document.getElementById("login-hint")!;
  const input = document.getElementById("login-input") as HTMLInputElement;
  overlay.style.display = "flex";
  if (error) {
    hint.textContent = error;
    hint.className = "hint error";
  }
  input.focus();
}
