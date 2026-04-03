const SKIN_COLORS = [
  "#e74c3c",
  "#2ecc71",
  "#3498db",
  "#f1c40f",
  "#9b59b6",
  "#e67e22",
  "#1abc9c",
  "#ecf0f1",
];

export class LoginScreen {
  private overlay: HTMLDivElement;
  private nameInput: HTMLInputElement;
  private selectedSkin: number = 0;
  private skinButtons: HTMLDivElement[] = [];
  private onPlay: ((name: string, skinID: number) => void) | null = null;
  private statusText: HTMLDivElement;
  private deathMessage: HTMLDivElement;
  private playButton: HTMLButtonElement;

  constructor() {
    this.overlay = document.createElement("div");
    this.overlay.style.cssText = `
      position: fixed;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      background: rgba(0, 0, 0, 0.8);
      z-index: 200;
    `;

    const panel = document.createElement("div");
    panel.style.cssText = `
      background: rgba(20, 20, 30, 0.95);
      border: 1px solid rgba(255, 255, 255, 0.15);
      border-radius: 12px;
      padding: 40px;
      text-align: center;
      color: white;
      font-family: Arial, sans-serif;
      min-width: 320px;
    `;

    // Title
    const title = document.createElement("h1");
    title.textContent = "SlitherMesh";
    title.style.cssText =
      "font-size: 36px; margin-bottom: 8px; color: #2ecc71;";
    panel.appendChild(title);

    const subtitle = document.createElement("p");
    subtitle.textContent = "Eat, grow, dominate across multiple servers!";
    subtitle.style.cssText =
      "color: rgba(255,255,255,0.5); margin-bottom: 24px; font-size: 14px;";
    panel.appendChild(subtitle);

    // Death message (hidden initially)
    this.deathMessage = document.createElement("div");
    this.deathMessage.style.cssText = `
      display: none;
      background: rgba(231, 76, 60, 0.2);
      border: 1px solid rgba(231, 76, 60, 0.4);
      border-radius: 6px;
      padding: 10px;
      margin-bottom: 16px;
      color: #e74c3c;
      font-size: 14px;
    `;
    panel.appendChild(this.deathMessage);

    // Name input
    this.nameInput = document.createElement("input");
    this.nameInput.type = "text";
    this.nameInput.placeholder = "Enter your name";
    this.nameInput.maxLength = 20;
    this.nameInput.style.cssText = `
      width: 100%;
      padding: 12px 16px;
      border: 1px solid rgba(255,255,255,0.2);
      border-radius: 6px;
      background: rgba(255,255,255,0.05);
      color: white;
      font-size: 16px;
      outline: none;
      margin-bottom: 20px;
    `;
    this.nameInput.addEventListener("focus", () => {
      this.nameInput.style.borderColor = "rgba(46,204,113,0.5)";
    });
    this.nameInput.addEventListener("blur", () => {
      this.nameInput.style.borderColor = "rgba(255,255,255,0.2)";
    });
    panel.appendChild(this.nameInput);

    // Skin selection label
    const skinLabel = document.createElement("div");
    skinLabel.textContent = "Choose your skin";
    skinLabel.style.cssText =
      "color: rgba(255,255,255,0.6); font-size: 13px; margin-bottom: 10px;";
    panel.appendChild(skinLabel);

    // Skin buttons
    const skinRow = document.createElement("div");
    skinRow.style.cssText =
      "display: flex; justify-content: center; gap: 8px; margin-bottom: 24px;";

    for (let i = 0; i < SKIN_COLORS.length; i++) {
      const btn = document.createElement("div");
      btn.style.cssText = `
        width: 36px;
        height: 36px;
        border-radius: 50%;
        background: ${SKIN_COLORS[i]};
        cursor: pointer;
        border: 3px solid transparent;
        transition: border-color 0.15s, transform 0.15s;
      `;
      if (i === 0) {
        btn.style.borderColor = "white";
        btn.style.transform = "scale(1.15)";
      }
      btn.addEventListener("click", () => this.selectSkin(i));
      skinRow.appendChild(btn);
      this.skinButtons.push(btn);
    }
    panel.appendChild(skinRow);

    // Play button
    const playBtn = document.createElement("button");
    this.playButton = playBtn;
    playBtn.textContent = "Play";
    playBtn.style.cssText = `
      width: 100%;
      padding: 14px;
      border: none;
      border-radius: 6px;
      background: #2ecc71;
      color: white;
      font-size: 18px;
      font-weight: bold;
      cursor: pointer;
      transition: background 0.15s;
    `;
    playBtn.addEventListener("mouseenter", () => {
      playBtn.style.background = "#27ae60";
    });
    playBtn.addEventListener("mouseleave", () => {
      playBtn.style.background = "#2ecc71";
    });
    playBtn.addEventListener("click", () => this.submit());
    panel.appendChild(playBtn);

    // Status text
    this.statusText = document.createElement("div");
    this.statusText.style.cssText =
      "color: rgba(255,255,255,0.4); font-size: 12px; margin-top: 12px;";
    panel.appendChild(this.statusText);

    this.overlay.appendChild(panel);

    // Enter key submits
    this.nameInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter") this.submit();
    });
  }

  private selectSkin(idx: number) {
    this.selectedSkin = idx;
    for (let i = 0; i < this.skinButtons.length; i++) {
      this.skinButtons[i].style.borderColor =
        i === idx ? "white" : "transparent";
      this.skinButtons[i].style.transform =
        i === idx ? "scale(1.15)" : "scale(1)";
    }
  }

  private submit() {
    const name = this.nameInput.value.trim();
    if (!name) {
      this.nameInput.style.borderColor = "#e74c3c";
      return;
    }
    if (this.onPlay) {
      this.onPlay(name, this.selectedSkin);
    }
  }

  setOnPlay(callback: (name: string, skinID: number) => void) {
    this.onPlay = callback;
  }

  setStatus(text: string) {
    this.statusText.textContent = text;
  }

  showDeath(message: string) {
    this.deathMessage.textContent = message;
    this.deathMessage.style.display = "block";
    this.playButton.textContent = "Play Again";
  }

  hideDeath() {
    this.deathMessage.style.display = "none";
    this.playButton.textContent = "Play";
  }

  show() {
    document.body.appendChild(this.overlay);
    this.nameInput.focus();
  }

  hide() {
    if (this.overlay.parentElement) {
      this.overlay.remove();
    }
  }

  destroy() {
    this.hide();
  }
}
