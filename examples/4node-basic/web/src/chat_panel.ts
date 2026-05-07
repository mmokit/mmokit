// Chat demo panel. Toggled with the 'c' key.
//
// Renders a tabbed chat UI in the top-right corner: one tab per joined
// channel from ChatChannelsHydratedEvent + dynamically-created DM tabs
// per partner. Input box detects leading '/' and dispatches slash
// commands; otherwise text is sent to the active tab via chatSend or
// chatSendDM.
//
// v1 slash commands implemented (all of which avoid username→userID
// resolution except /w, which uses a client-side directory built from
// member-join events):
//   /w <user> <text>           — DM (alias /whisper /dm /msg)
//   /join <slug> [pw]          — join a custom channel
//   /leave                     — leave the active custom channel
//   /create <slug> [pw]        — create a custom channel
//   /topic <text>              — set topic of the active channel
//   /rename <new-slug>         — rename the active channel
//   /slowmode <seconds>        — set slowmode on the active channel
//   /delete <msg_id>           — delete a message
//
// Mute / kick / ban / unmute / unban / op / deop need username→userID
// resolution and full role checks; for v1 they're console-only.

import {
  ChatSendRequest,
  ChatSendDMRequest,
  ChatJoinRequest,
  ChatLeaveRequest,
  ChatCreateRequest,
  ChatSetTopicRequest,
  ChatRenameChannelRequest,
  ChatSetSlowModeRequest,
  ChatDeleteMessageRequest,
} from "../sdk/operations.js";
import {
  ChatMessageEvent,
  ChatDMEvent,
  ChatMemberJoinedEvent,
  ChatMemberLeftEvent,
  ChatChannelsHydratedEvent,
  ChatChannelUpdatedEvent,
  ChatChannelGoneEvent,
  ChatMessageDeletedEvent,
  ChatMutedEvent,
  ChatUnmutedEvent,
  ChatKickedEvent,
  ChatBannedEvent,
} from "../sdk/broadcasts.js";
import type { BasicClient } from "../sdk/client.js";

interface ChannelTab {
  kind: "channel";
  channelID: string;
  slug: string;
  topic: string;
  channelKind: number; // ChannelKind enum from server
  messages: HTMLElement;
}

interface DMTab {
  kind: "dm";
  partnerUserID: string;
  partnerUsername: string;
  messages: HTMLElement;
}

type Tab = ChannelTab | DMTab;

export function mountChatPanel(client: BasicClient): void {
  // --- Layout: collapsible top-right panel below echo (echo lives in
  //   the same corner so we shift down ~44px). Tabs across the top,
  //   message area, input box at the bottom.
  const root = document.createElement("div");
  root.id = "chat-panel";
  root.style.cssText = `
    position: fixed; top: 380px; right: 8px; width: 380px; height: 320px;
    background: rgba(15, 18, 30, 0.92); color: #cce; font-family: monospace;
    font-size: 12px; border: 1px solid #335; border-radius: 4px;
    box-shadow: 0 6px 20px rgba(0,0,0,0.4);
    display: none; z-index: 9999; flex-direction: column;
  `;
  root.innerHTML = `
    <div style="display:flex;justify-content:space-between;align-items:center;padding:6px 8px;border-bottom:1px solid #335;">
      <b style="color:#9cf">Chat (press 'c' to toggle)</b>
      <button id="cp-close" style="background:none;color:#cce;border:1px solid #446;cursor:pointer;padding:0 6px;">×</button>
    </div>
    <div id="cp-tabs" style="display:flex;flex-wrap:wrap;gap:2px;padding:4px 6px;border-bottom:1px solid #335;background:#181b2a;"></div>
    <div id="cp-topic" style="padding:4px 8px;color:#79c;font-style:italic;border-bottom:1px solid #223;font-size:11px;min-height:1em;"></div>
    <div id="cp-messages-wrap" style="flex:1;overflow:hidden;position:relative;"></div>
    <div style="display:flex;padding:6px;border-top:1px solid #335;gap:4px;">
      <input id="cp-input" placeholder="Type a message or /command..." style="flex:1;background:#1a1d2a;color:#cce;border:1px solid #446;padding:3px 6px;font-family:monospace;"/>
      <button id="cp-send" style="background:#234;color:#cce;border:1px solid #446;cursor:pointer;padding:3px 10px;">send</button>
    </div>
    <div id="cp-status" style="padding:2px 8px;color:#888;font-size:11px;min-height:1em;"></div>
  `;
  document.body.appendChild(root);

  const $ = (sel: string) => root.querySelector(sel) as HTMLElement;
  const tabsEl = $("#cp-tabs");
  const messagesWrap = $("#cp-messages-wrap");
  const topicEl = $("#cp-topic");
  const inputEl = $("#cp-input") as HTMLInputElement;
  const sendBtn = $("#cp-send") as HTMLButtonElement;
  const statusEl = $("#cp-status");

  ($("#cp-close") as HTMLButtonElement).onclick = () => {
    root.style.display = "none";
  };

  // --- State ---
  const tabs = new Map<string, Tab>(); // tabKey → Tab
  // Client-side directory of users we've seen via ChatMemberJoinedEvent.
  // Populated continuously as members join channels. Used by /w to
  // resolve username → userID.
  const userDir = new Map<string, string>(); // username (lowercase) → userID
  let activeKey: string | null = null;

  function tabKey(t: Tab): string {
    return t.kind === "channel" ? `c:${t.channelID}` : `d:${t.partnerUserID}`;
  }

  function setStatus(msg: string, color = "#888"): void {
    statusEl.textContent = msg;
    statusEl.style.color = color;
  }

  function makeMessageContainer(): HTMLElement {
    const el = document.createElement("div");
    el.style.cssText = `
      position: absolute; inset: 0; overflow-y: auto;
      padding: 4px 8px; display: none;
    `;
    messagesWrap.appendChild(el);
    return el;
  }

  function ensureChannelTab(
    channelID: string,
    slug: string,
    topic: string,
    kind: number,
  ): ChannelTab {
    const key = `c:${channelID}`;
    const existing = tabs.get(key);
    if (existing && existing.kind === "channel") {
      existing.slug = slug;
      existing.topic = topic;
      existing.channelKind = kind;
      return existing;
    }
    const tab: ChannelTab = {
      kind: "channel",
      channelID,
      slug,
      topic,
      channelKind: kind,
      messages: makeMessageContainer(),
    };
    tabs.set(key, tab);
    renderTabs();
    return tab;
  }

  function ensureDMTab(partnerUserID: string, partnerUsername: string): DMTab {
    const key = `d:${partnerUserID}`;
    const existing = tabs.get(key);
    if (existing && existing.kind === "dm") {
      if (partnerUsername) existing.partnerUsername = partnerUsername;
      return existing;
    }
    const tab: DMTab = {
      kind: "dm",
      partnerUserID,
      partnerUsername: partnerUsername || partnerUserID,
      messages: makeMessageContainer(),
    };
    tabs.set(key, tab);
    renderTabs();
    return tab;
  }

  function setActive(key: string): void {
    if (!tabs.has(key)) return;
    activeKey = key;
    for (const [k, t] of tabs) {
      t.messages.style.display = k === key ? "block" : "none";
    }
    const t = tabs.get(key)!;
    topicEl.textContent =
      t.kind === "channel" ? (t.topic || `#${t.slug}`) : `DM: ${t.partnerUsername}`;
    renderTabs();
    inputEl.focus();
  }

  function renderTabs(): void {
    tabsEl.innerHTML = "";
    for (const [k, t] of tabs) {
      const btn = document.createElement("button");
      const label = t.kind === "channel" ? `#${t.slug}` : `@${t.partnerUsername}`;
      btn.textContent = label;
      const isActive = k === activeKey;
      btn.style.cssText = `
        background: ${isActive ? "#345" : "#1a1d2a"};
        color: ${isActive ? "#fff" : "#9cc"};
        border: 1px solid #446;
        padding: 2px 8px;
        cursor: pointer;
        font-family: monospace;
        font-size: 11px;
      `;
      btn.onclick = () => setActive(k);
      tabsEl.appendChild(btn);
    }
  }

  function appendMessage(
    container: HTMLElement,
    senderLabel: string,
    body: string,
    opts: { system?: boolean; meta?: boolean } = {},
  ): void {
    const line = document.createElement("div");
    line.style.cssText = `padding: 1px 0; word-wrap: break-word; line-height: 1.35;`;
    if (opts.system) {
      line.style.fontStyle = "italic";
      line.style.color = "#fc6"; // amber for system messages
      line.textContent = body;
    } else if (opts.meta) {
      line.style.color = "#688";
      line.style.fontSize = "10px";
      line.textContent = body;
    } else {
      const sender = document.createElement("span");
      sender.style.color = "#9cf";
      sender.style.fontWeight = "bold";
      sender.textContent = senderLabel + ": ";
      line.appendChild(sender);
      line.appendChild(document.createTextNode(body));
    }
    container.appendChild(line);
    // Scroll to bottom if the user is already there.
    container.scrollTop = container.scrollHeight;
  }

  function activeTab(): Tab | null {
    return activeKey ? tabs.get(activeKey) ?? null : null;
  }

  // --- Server event subscriptions ---

  client.onChatChannelsHydratedEvent((msg: ChatChannelsHydratedEvent) => {
    for (const ch of msg.channels) {
      ensureChannelTab(ch.channelID, ch.slug, ch.topic, ch.kind);
    }
    if (!activeKey && tabs.size > 0) {
      // Default to the first channel, preferring 'world' if present.
      let first: string | null = null;
      for (const [k, t] of tabs) {
        if (t.kind === "channel" && t.slug === "world") {
          first = k;
          break;
        }
        if (!first) first = k;
      }
      if (first) setActive(first);
    }
  });

  client.onChatMessageEvent((msg: ChatMessageEvent) => {
    const key = `c:${msg.channelID}`;
    const t = tabs.get(key);
    if (!t || t.kind !== "channel") return; // message for a channel we don't track
    if (msg.senderUserID === "") {
      // System broadcast (e.g. SYSTEM:, /broadcast).
      appendMessage(t.messages, "", msg.body, { system: true });
    } else {
      appendMessage(t.messages, msg.senderUsername || msg.senderUserID, msg.body);
    }
  });

  client.onChatDMEvent((msg: ChatDMEvent) => {
    // DMs always come from the OTHER side; create-or-update a DM tab
    // keyed on the sender's user_id.
    const tab = ensureDMTab(msg.senderUserID, msg.senderUsername);
    appendMessage(tab.messages, msg.senderUsername || msg.senderUserID, msg.body);
    // Track sender in the directory for future /w resolution.
    if (msg.senderUsername) {
      userDir.set(msg.senderUsername.toLowerCase(), msg.senderUserID);
    }
  });

  client.onChatMemberJoinedEvent((msg: ChatMemberJoinedEvent) => {
    if (msg.username) {
      userDir.set(msg.username.toLowerCase(), msg.userID);
    }
    const t = tabs.get(`c:${msg.channelID}`);
    if (t && t.kind === "channel") {
      appendMessage(t.messages, "", `${msg.username || msg.userID} joined`, { meta: true });
    }
  });

  client.onChatMemberLeftEvent((msg: ChatMemberLeftEvent) => {
    const t = tabs.get(`c:${msg.channelID}`);
    if (t && t.kind === "channel") {
      appendMessage(t.messages, "", `${msg.userID} left`, { meta: true });
    }
  });

  client.onChatChannelUpdatedEvent((msg: ChatChannelUpdatedEvent) => {
    const ch = msg.channel;
    const tab = ensureChannelTab(ch.channelID, ch.slug, ch.topic, ch.kind);
    if (activeKey === tabKey(tab)) {
      topicEl.textContent = tab.topic || `#${tab.slug}`;
    }
    renderTabs();
  });

  client.onChatChannelGoneEvent((msg: ChatChannelGoneEvent) => {
    const key = `c:${msg.channelID}`;
    const tab = tabs.get(key);
    if (!tab) return;
    tab.messages.remove();
    tabs.delete(key);
    if (activeKey === key) {
      activeKey = null;
      const next = tabs.keys().next().value;
      if (next) setActive(next);
      else topicEl.textContent = "";
    }
    renderTabs();
  });

  client.onChatMessageDeletedEvent((msg: ChatMessageDeletedEvent) => {
    const t = tabs.get(`c:${msg.channelID}`);
    if (t && t.kind === "channel") {
      appendMessage(t.messages, "", `[message ${msg.msgID} deleted]`, { meta: true });
    }
  });

  client.onChatMutedEvent((msg: ChatMutedEvent) => {
    const tab = msg.channelID ? tabs.get(`c:${msg.channelID}`) : null;
    const where = msg.channelID ? "in this channel" : "globally";
    const reason = msg.reason ? ` (${msg.reason})` : "";
    if (tab && tab.kind === "channel") {
      appendMessage(tab.messages, "", `you were muted ${where}${reason}`, { system: true });
    } else {
      setStatus(`muted ${where}${reason}`, "#fc6");
    }
  });

  client.onChatUnmutedEvent((msg: ChatUnmutedEvent) => {
    const tab = msg.channelID ? tabs.get(`c:${msg.channelID}`) : null;
    if (tab && tab.kind === "channel") {
      appendMessage(tab.messages, "", "you were unmuted", { system: true });
    } else {
      setStatus("unmuted", "#9c9");
    }
  });

  client.onChatKickedEvent((msg: ChatKickedEvent) => {
    const t = tabs.get(`c:${msg.channelID}`);
    const reason = msg.reason ? ` (${msg.reason})` : "";
    if (t && t.kind === "channel") {
      appendMessage(t.messages, "", `you were kicked${reason}`, { system: true });
    }
  });

  client.onChatBannedEvent((msg: ChatBannedEvent) => {
    const t = tabs.get(`c:${msg.channelID}`);
    const reason = msg.reason ? ` (${msg.reason})` : "";
    if (t && t.kind === "channel") {
      appendMessage(t.messages, "", `you were banned${reason}`, { system: true });
    }
  });

  // --- Slash command parsing + dispatch ---

  async function handleInput(text: string): Promise<void> {
    if (!text.trim()) return;

    if (text.startsWith("/")) {
      await handleSlash(text);
      return;
    }

    // Plain text — send to active tab.
    const t = activeTab();
    if (!t) {
      setStatus("no active channel", "#f88");
      return;
    }
    try {
      if (t.kind === "channel") {
        const resp = await client.chatSend(new ChatSendRequest({
          channelID: t.channelID,
          body: text,
        }));
        if (resp.errorBlock.errorCode !== 0) {
          setStatus(`error: ${resp.errorBlock.errorMessage}`, "#f88");
        } else {
          setStatus("");
        }
      } else {
        const resp = await client.chatSendDM(new ChatSendDMRequest({
          recipientUserID: t.partnerUserID,
          body: text,
        }));
        if (resp.errorBlock.errorCode !== 0) {
          setStatus(`error: ${resp.errorBlock.errorMessage}`, "#f88");
        } else {
          // Echo the locally-sent DM into the tab — server doesn't
          // fanout the sender's own DM.
          appendMessage(t.messages, "me", text);
          setStatus("");
        }
      }
    } catch (e) {
      setStatus(`error: ${e instanceof Error ? e.message : String(e)}`, "#f88");
    }
  }

  async function handleSlash(text: string): Promise<void> {
    const parts = text.slice(1).split(/\s+/);
    const cmd = (parts.shift() ?? "").toLowerCase();
    const t = activeTab();
    try {
      switch (cmd) {
        case "w":
        case "whisper":
        case "dm":
        case "msg": {
          const target = (parts.shift() ?? "").toLowerCase();
          const body = parts.join(" ");
          if (!target || !body) {
            setStatus("usage: /w <user> <text>", "#fc6");
            return;
          }
          const userID = userDir.get(target);
          if (!userID) {
            setStatus(`user '${target}' not found in any of your channels`, "#fc6");
            return;
          }
          const resp = await client.chatSendDM(new ChatSendDMRequest({
            recipientUserID: userID,
            body,
          }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`error: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          // Open the DM tab and echo the message locally.
          const dmTab = ensureDMTab(userID, target);
          appendMessage(dmTab.messages, "me", body);
          setActive(tabKey(dmTab));
          setStatus("");
          return;
        }
        case "join": {
          const slug = parts.shift() ?? "";
          const password = parts.shift() ?? "";
          if (!slug) {
            setStatus("usage: /join <slug> [pw]", "#fc6");
            return;
          }
          const resp = await client.chatJoin(new ChatJoinRequest({ slug, password }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`join failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          const ch = resp.channel;
          const tab = ensureChannelTab(ch.channelID, ch.slug, ch.topic, ch.kind);
          setActive(tabKey(tab));
          setStatus("");
          return;
        }
        case "leave": {
          if (!t || t.kind !== "channel") {
            setStatus("usage: /leave (no active channel)", "#fc6");
            return;
          }
          const resp = await client.chatLeave(new ChatLeaveRequest({ channelID: t.channelID }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`leave failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          const key = tabKey(t);
          t.messages.remove();
          tabs.delete(key);
          activeKey = null;
          const next = tabs.keys().next().value;
          if (next) setActive(next);
          else topicEl.textContent = "";
          renderTabs();
          setStatus("");
          return;
        }
        case "create": {
          const slug = parts.shift() ?? "";
          const password = parts.shift() ?? "";
          if (!slug) {
            setStatus("usage: /create <slug> [pw]", "#fc6");
            return;
          }
          const resp = await client.chatCreate(new ChatCreateRequest({ slug, password, topic: "" }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`create failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          const ch = resp.channel;
          const tab = ensureChannelTab(ch.channelID, ch.slug, ch.topic, ch.kind);
          setActive(tabKey(tab));
          setStatus("");
          return;
        }
        case "topic": {
          if (!t || t.kind !== "channel") {
            setStatus("usage: /topic <text> (need active channel)", "#fc6");
            return;
          }
          const topic = parts.join(" ");
          const resp = await client.chatSetTopic(new ChatSetTopicRequest({
            channelID: t.channelID,
            topic,
          }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`topic failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          setStatus("");
          return;
        }
        case "rename": {
          if (!t || t.kind !== "channel") {
            setStatus("usage: /rename <new-slug> (need active channel)", "#fc6");
            return;
          }
          const newSlug = parts.shift() ?? "";
          if (!newSlug) {
            setStatus("usage: /rename <new-slug>", "#fc6");
            return;
          }
          const resp = await client.chatRenameChannel(new ChatRenameChannelRequest({
            channelID: t.channelID,
            newSlug,
          }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`rename failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          setStatus("");
          return;
        }
        case "slowmode": {
          if (!t || t.kind !== "channel") {
            setStatus("usage: /slowmode <seconds> (need active channel)", "#fc6");
            return;
          }
          const seconds = parseInt(parts.shift() ?? "0", 10);
          if (Number.isNaN(seconds)) {
            setStatus("usage: /slowmode <seconds>", "#fc6");
            return;
          }
          const resp = await client.chatSetSlowMode(new ChatSetSlowModeRequest({
            channelID: t.channelID,
            seconds,
          }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`slowmode failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          setStatus("");
          return;
        }
        case "delete": {
          if (!t || t.kind !== "channel") {
            setStatus("usage: /delete <msg_id> (need active channel)", "#fc6");
            return;
          }
          const msgID = parts.shift() ?? "";
          if (!msgID) {
            setStatus("usage: /delete <msg_id>", "#fc6");
            return;
          }
          const resp = await client.chatDeleteMessage(new ChatDeleteMessageRequest({
            msgID,
            channelID: t.channelID,
          }));
          if (resp.errorBlock.errorCode !== 0) {
            setStatus(`delete failed: ${resp.errorBlock.errorMessage}`, "#f88");
            return;
          }
          setStatus("");
          return;
        }
        case "help": {
          setStatus(
            "/w /join /leave /create /topic /rename /slowmode /delete (mute/kick/ban via console)",
            "#9cc",
          );
          return;
        }
        default:
          setStatus(`unknown command: /${cmd} — try /help`, "#fc6");
      }
    } catch (e) {
      setStatus(`error: ${e instanceof Error ? e.message : String(e)}`, "#f88");
    }
  }

  // --- Wire up input ---

  sendBtn.onclick = async () => {
    const text = inputEl.value;
    inputEl.value = "";
    await handleInput(text);
  };

  inputEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendBtn.click();
    }
  });

  // 'c' toggles the panel — only outside of input fields.
  window.addEventListener("keydown", (e) => {
    if (e.key !== "c") return;
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
    root.style.display = root.style.display === "none" ? "flex" : "none";
    if (root.style.display === "flex") inputEl.focus();
  });
}
