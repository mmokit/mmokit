import type { KillFeedEntry } from "./state";

const DISPLAY_DURATION = 5000;
const FADE_DURATION = 1000;

export class KillFeed {
  private container: HTMLDivElement;

  constructor() {
    this.container = document.createElement("div");
    this.container.style.cssText = `
      position: fixed;
      top: 10px;
      left: 10px;
      width: 280px;
      color: white;
      font-family: Arial, sans-serif;
      font-size: 12px;
      pointer-events: none;
      z-index: 100;
      display: none;
    `;
    document.body.appendChild(this.container);
  }

  update(entries: KillFeedEntry[]) {
    const now = performance.now();
    let html = "";

    for (const entry of entries) {
      const age = now - entry.time;
      if (age > DISPLAY_DURATION + FADE_DURATION) continue;

      let opacity = 1;
      if (age > DISPLAY_DURATION) {
        opacity = 1 - (age - DISPLAY_DURATION) / FADE_DURATION;
      }

      const massStr = Math.floor(entry.mass).toString();
      html += `<div style="opacity:${opacity.toFixed(2)};background:rgba(0,0,0,0.5);padding:4px 8px;border-radius:3px;margin-bottom:3px;border-left:3px solid #e74c3c;">`;
      html += `<span style="color:#ff6b6b;font-weight:bold;">${this.escapeHtml(entry.killer)}</span>`;
      html += ` killed `;
      html += `<span style="color:#ffaa6b;">${this.escapeHtml(entry.victim)}</span>`;
      html += ` <span style="color:rgba(255,255,255,0.5);">(${massStr} mass)</span>`;
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
