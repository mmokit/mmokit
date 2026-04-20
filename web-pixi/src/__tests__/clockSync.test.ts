import { describe, test, expect } from "bun:test";
import { newClockSync, observeServerTime, estimatedServerNow } from "../clockSync";

describe("ClockSync", () => {
  test("initializes on first observation", () => {
    const c = newClockSync();
    expect(c.initialized).toBe(false);

    observeServerTime(c, 10_000, 5_000);

    expect(c.initialized).toBe(true);
    expect(c.offsetMs).toBe(5_000);
  });

  test("exponentially smooths successive observations", () => {
    const c = newClockSync();
    observeServerTime(c, 10_000, 5_000); // offset = 5000
    observeServerTime(c, 10_100, 5_050); // instant = 5050

    // α = 0.1, so offset = 0.9 * 5000 + 0.1 * 5050 = 5005.
    expect(c.offsetMs).toBeCloseTo(5_005, 1);
  });

  test("estimatedServerNow = clientNow + smoothed offset", () => {
    const c = newClockSync();
    observeServerTime(c, 10_000, 5_000);
    expect(estimatedServerNow(c, 6_000)).toBe(11_000);
  });

  test("converges toward a new steady offset", () => {
    const c = newClockSync();
    observeServerTime(c, 0, 0); // offset = 0, initialized
    // Feed 100 observations with a true offset of 1000.
    for (let i = 1; i <= 100; i++) {
      observeServerTime(c, 1_000 + i, i); // instant = 1000 every time
    }
    expect(c.offsetMs).toBeCloseTo(1_000, 0);
  });
});
