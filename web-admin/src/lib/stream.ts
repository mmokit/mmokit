// One global multiplexed EventSource. Every panel that wants live data calls
// stream.subscribe(topic, handler); the lib shares one HTTP connection and
// reopens it (with the merged topic set) on subscribe / unsubscribe.

type Handler = (msg: unknown) => void;

const subs = new Map<string, Set<Handler>>();
let es: EventSource | null = null;
let reopenTimer: ReturnType<typeof setTimeout> | null = null;
let backoffMs = 200;

function topicsList(): string[] {
  return [...subs.keys()].filter((t) => (subs.get(t)?.size ?? 0) > 0);
}

function reopen(): void {
  if (es) {
    es.close();
    es = null;
  }
  const topics = topicsList();
  if (topics.length === 0) {
    return;
  }
  const url = `/admin/api/stream?topics=${encodeURIComponent(topics.join(","))}`;
  const next = new EventSource(url);
  es = next;

  // Server sends `event: <topic>\ndata: <json>\n\n` so we attach one
  // listener per known topic; new topics added later trigger another reopen.
  for (const t of topics) {
    next.addEventListener(t, (evt) => {
      let data: unknown;
      try {
        data = JSON.parse((evt as MessageEvent).data);
      } catch {
        return;
      }
      const set = subs.get(t);
      if (!set) return;
      for (const h of set) h(data);
    });
  }

  next.onopen = () => {
    backoffMs = 200; // reset on success
  };
  next.onerror = () => {
    next.close();
    es = null;
    if (topicsList().length === 0) return;
    if (reopenTimer) return;
    const delay = Math.min(backoffMs, 5000) + Math.floor(Math.random() * 100);
    reopenTimer = setTimeout(() => {
      reopenTimer = null;
      backoffMs = Math.min(backoffMs * 2, 5000);
      reopen();
    }, delay);
  };
}

function ensureOpen(): void {
  if (!es && topicsList().length > 0) reopen();
}

export const stream = {
  subscribe(topic: string, handler: Handler): () => void {
    const beforeTopics = topicsList().sort().join(",");
    let set = subs.get(topic);
    if (!set) {
      set = new Set();
      subs.set(topic, set);
    }
    set.add(handler);
    const afterTopics = topicsList().sort().join(",");
    if (beforeTopics !== afterTopics) reopen();
    else ensureOpen();
    return () => {
      const s = subs.get(topic);
      if (!s) return;
      s.delete(handler);
      if (s.size === 0) {
        subs.delete(topic);
        const remaining = topicsList();
        if (remaining.length === 0) {
          es?.close();
          es = null;
        } else {
          reopen();
        }
      }
    };
  },

  // Test-only: close the stream and clear subscriptions.
  reset(): void {
    es?.close();
    es = null;
    subs.clear();
    if (reopenTimer) {
      clearTimeout(reopenTimer);
      reopenTimer = null;
    }
    backoffMs = 200;
  },
};
