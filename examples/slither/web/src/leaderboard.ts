import type { LeaderEntry } from "./state";

const SKIN_CSS_COLORS = [
  "#e74c3c", "#2ecc71", "#3498db", "#f1c40f",
  "#9b59b6", "#e67e22", "#1abc9c", "#ecf0f1",
];

export class Leaderboard {
  private container: HTMLDivElement;
  private myName: string = "";

  constructor() {
    this.container = document.createElement("div");
    this.container.style.cssText = `
      position: fixed;
      top: 10px;
      right: 10px;
      width: 200px;
      background: rgba(0, 0, 0, 0.6);
      border: 1px solid rgba(255, 255, 255, 0.15);
      border-radius: 6px;
      padding: 12px;
      color: white;
      font-family: Arial, sans-serif;
      font-size: 13px;
      pointer-events: none;
      z-index: 100;
      display: none;
    `;
    document.body.appendChild(this.container);
  }

  setMyName(name: string) {
    this.myName = name.toLowerCase();
  }

  update(entries: LeaderEntry[]) {
    let html = '<div style="font-weight:bold;font-size:14px;margin-bottom:8px;text-align:center;">Leaderboard</div>';

    for (let i = 0; i < Math.min(entries.length, 10); i++) {
      const e = entries[i];
      const isMe = e.name.toLowerCase() === this.myName;
      const colorDot = SKIN_CSS_COLORS[e.skinID % SKIN_CSS_COLORS.length];
      const bg = isMe ? "rgba(255,255,255,0.1)" : "transparent";
      const weight = isMe ? "bold" : "normal";
      html += `<div style="display:flex;align-items:center;padding:2px 4px;border-radius:3px;background:${bg};margin-bottom:2px;">`;
      html += `<span style="width:18px;color:rgba(255,255,255,0.5);">${i + 1}.</span>`;
      html += `<span style="width:8px;height:8px;border-radius:50%;background:${colorDot};margin-right:6px;flex-shrink:0;"></span>`;
      html += `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:${weight};">${this.escapeHtml(e.name)}</span>`;
      html += `<span style="color:rgba(255,255,255,0.6);margin-left:4px;">${Math.floor(e.mass)}</span>`;
      html += `</div>`;
    }

    this.container.innerHTML = html;
  }

  private escapeHtml(text: string): string {
    const div = document.createElement("div");
    div.textContent = text;
    return div.innerHTML;
  }

  show() {
    this.container.style.display = "block";
  }

  hide() {
    this.container.style.display = "none";
  }

  destroy() {
    this.container.remove();
  }
}
