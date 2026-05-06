// Echo demo service test panel. Toggled with the 'e' key.
//
// Renders three sections — PING, PERSIST, FETCH — that exercise the
// pluggable services framework end-to-end. Responses include the
// instance ID that handled the request, which makes hash-affinity
// routing and cross-instance DB consistency visible at a glance.

import {
  EchoPingRequest,
  EchoPersistRequest,
  EchoFetchRequest,
} from "../sdk/operations.js";
import type { BasicClient } from "../sdk/client.js";

export function mountEchoPanel(client: BasicClient): void {
  // --- DOM ---
  const root = document.createElement("div");
  root.id = "echo-panel";
  root.style.cssText = `
    position: fixed; top: 8px; right: 8px; width: 360px;
    background: rgba(15, 18, 30, 0.92); color: #cce; font-family: monospace;
    font-size: 12px; padding: 10px; border: 1px solid #335;
    border-radius: 4px; box-shadow: 0 6px 20px rgba(0,0,0,0.4);
    display: none; z-index: 9999;
  `;
  root.innerHTML = `
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px;">
      <b style="color:#9cf">Echo Service Test (press 'e' to toggle)</b>
      <button id="ep-close" style="background:none;color:#cce;border:1px solid #446;cursor:pointer;padding:0 6px;">×</button>
    </div>

    <div style="border-bottom:1px solid #335;padding:6px 0;">
      <div style="color:#9c9;margin-bottom:3px;">PING — visualizes routing affinity</div>
      <input id="ep-ping" value="hello world" style="width:60%;background:#1a1d2a;color:#cce;border:1px solid #446;padding:2px 4px;"/>
      <button id="ep-ping-btn" style="background:#234;color:#cce;border:1px solid #446;cursor:pointer;padding:2px 8px;">send</button>
      <div id="ep-ping-out" style="color:#9c9;margin-top:4px;min-height:1.2em;"></div>
    </div>

    <div style="border-bottom:1px solid #335;padding:6px 0;">
      <div style="color:#9c9;margin-bottom:3px;">PERSIST — write to demo_echo</div>
      <input id="ep-key" placeholder="key" value="demo" style="width:35%;background:#1a1d2a;color:#cce;border:1px solid #446;padding:2px 4px;"/>
      <input id="ep-val" placeholder="value" value="hi" style="width:35%;background:#1a1d2a;color:#cce;border:1px solid #446;padding:2px 4px;margin-left:2px;"/>
      <button id="ep-persist-btn" style="background:#234;color:#cce;border:1px solid #446;cursor:pointer;padding:2px 8px;margin-left:2px;">send</button>
      <div id="ep-persist-out" style="color:#9c9;margin-top:4px;min-height:1.2em;"></div>
    </div>

    <div style="padding:6px 0;">
      <div style="color:#9c9;margin-bottom:3px;">FETCH — read from demo_echo</div>
      <input id="ep-fkey" placeholder="key" value="demo" style="width:60%;background:#1a1d2a;color:#cce;border:1px solid #446;padding:2px 4px;"/>
      <button id="ep-fetch-btn" style="background:#234;color:#cce;border:1px solid #446;cursor:pointer;padding:2px 8px;">send</button>
      <div id="ep-fetch-out" style="color:#9c9;margin-top:4px;min-height:1.2em;"></div>
    </div>
  `;
  document.body.appendChild(root);

  const $ = (sel: string) => root.querySelector(sel) as HTMLElement;
  const out = (sel: string, text: string, color = "#9c9") => {
    const el = $(sel);
    el.textContent = text;
    el.style.color = color;
  };

  ($("#ep-close") as HTMLButtonElement).onclick = () => {
    root.style.display = "none";
  };

  ($("#ep-ping-btn") as HTMLButtonElement).onclick = async () => {
    const msg = ($("#ep-ping") as HTMLInputElement).value;
    out("#ep-ping-out", "...", "#79c");
    try {
      const resp = await client.echoPing(new EchoPingRequest({ msg }));
      out("#ep-ping-out", `← msg="${resp.msg}" instance=${resp.instanceID}`);
    } catch (e) {
      out("#ep-ping-out", `error: ${e instanceof Error ? e.message : String(e)}`, "#f88");
    }
  };

  ($("#ep-persist-btn") as HTMLButtonElement).onclick = async () => {
    const key = ($("#ep-key") as HTMLInputElement).value;
    const value = ($("#ep-val") as HTMLInputElement).value;
    out("#ep-persist-out", "...", "#79c");
    try {
      const resp = await client.echoPersist(new EchoPersistRequest({ key, value }));
      out("#ep-persist-out", `← ok=${resp.ok} instance=${resp.instanceID}`);
    } catch (e) {
      out("#ep-persist-out", `error: ${e instanceof Error ? e.message : String(e)}`, "#f88");
    }
  };

  ($("#ep-fetch-btn") as HTMLButtonElement).onclick = async () => {
    const key = ($("#ep-fkey") as HTMLInputElement).value;
    out("#ep-fetch-out", "...", "#79c");
    try {
      const resp = await client.echoFetch(new EchoFetchRequest({ key }));
      const ts = resp.foundAtMs ? new Date(Number(resp.foundAtMs)).toISOString() : "(missing)";
      out("#ep-fetch-out", `← value="${resp.value}" foundAt=${ts} instance=${resp.instanceID}`);
    } catch (e) {
      out("#ep-fetch-out", `error: ${e instanceof Error ? e.message : String(e)}`, "#f88");
    }
  };

  // 'e' toggles the panel — but only outside of input fields so typing
  // 'e' in a textbox doesn't dismiss the panel mid-keystroke.
  window.addEventListener("keydown", (e) => {
    if (e.key !== "e") return;
    if (e.target instanceof HTMLInputElement) return;
    root.style.display = root.style.display === "none" ? "block" : "none";
  });
}
