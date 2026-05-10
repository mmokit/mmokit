import { describe, it, expect, beforeEach } from "vitest";
import { stream } from "./stream";

type TestES = {
  url: string;
  readyState: number;
  emit: (type: string, data: unknown) => void;
};

function lastES(): TestES {
  const es = (globalThis as { __lastES?: TestES }).__lastES;
  if (!es) throw new Error("no EventSource constructed");
  return es;
}

describe("stream", () => {
  beforeEach(() => {
    stream.reset();
    delete (globalThis as { __lastES?: unknown }).__lastES;
  });

  it("subscribes to a topic and receives parsed events", async () => {
    const received: unknown[] = [];
    stream.subscribe("cells", (msg) => received.push(msg));
    await Promise.resolve();

    lastES().emit("cells", [{ id: "0_0", load: 0.5 }]);
    await Promise.resolve();
    expect(received).toEqual([[{ id: "0_0", load: 0.5 }]]);
  });

  it("multiplexes multiple topics over one EventSource", async () => {
    const seen: string[] = [];
    stream.subscribe("cells", () => seen.push("cells"));
    stream.subscribe("hosts", () => seen.push("hosts"));
    await Promise.resolve();

    const es = lastES();
    expect(es.url).toContain("topics=");
    expect(es.url).toMatch(/cells/);
    expect(es.url).toMatch(/hosts/);

    es.emit("cells", []);
    es.emit("hosts", []);
    await Promise.resolve();
    expect(seen).toEqual(["cells", "hosts"]);
  });

  it("unsubscribe stops delivery and closes ES when last sub removed", async () => {
    let count = 0;
    const off = stream.subscribe("cells", () => count++);
    await Promise.resolve();
    const es = lastES();
    es.emit("cells", []);
    await Promise.resolve();
    expect(count).toBe(1);

    off();
    es.emit("cells", []);
    await Promise.resolve();
    expect(count).toBe(1);
    expect(es.readyState).toBe(2 /* CLOSED */);
  });
});
