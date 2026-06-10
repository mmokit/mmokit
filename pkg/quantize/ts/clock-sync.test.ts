import { describe, test, expect } from "bun:test";
import { newClockSync, observeServerTime, estimatedServerNow } from "./clock-sync";

// The golden manifest is authored by cmd/csharp-golden (Go reference).
const golden = require("../../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json");

describe("ClockSync golden parity (TS === Go === C#)", () => {
  test("window matches", () => {
    expect(golden.clockSync.window).toBe(40);
  });

  test("reproduces every golden offset", () => {
    const c = newClockSync();
    for (const o of golden.clockSync.observations) {
      observeServerTime(c, o.serverMs, o.clientNowMs);
      expect(c.initialized).toBe(true);
      expect(c.offsetMs).toBeCloseTo(o.expectedOffsetMs, 6);
      expect(estimatedServerNow(c, o.clientNowMs)).toBeCloseTo(o.clientNowMs + o.expectedOffsetMs, 6);
    }
  });
});
