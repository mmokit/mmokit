import { describe, test, expect } from "bun:test";
import { newClockSync, observeServerTime, estimatedServerNow } from "../clockSync";

describe("ClockSync (max-instant)", () => {
  test("initializes on first observation", () => {
    const c = newClockSync();
    expect(c.initialized).toBe(false);

    observeServerTime(c, 10_000, 5_000);

    expect(c.initialized).toBe(true);
    expect(c.offsetMs).toBe(5_000);
  });

  test("tracks the maximum (least-negative) instant in the window", () => {
    const c = newClockSync();
    observeServerTime(c, 10_000, 5_000);  // instant = 5000
    observeServerTime(c, 10_100, 5_050);  // instant = 5050  (better)

    // Max over both observations is 5050. Old EWMA would smooth to 5005.
    expect(c.offsetMs).toBe(5_050);
  });

  test("ignores bursty late observations (more negative instants)", () => {
    const c = newClockSync();
    // Establish a clean baseline.
    observeServerTime(c, 10_000, 5_000);  // instant = 5000

    // Burst: 6 frames where clientNow stays fixed and serverStamp climbs.
    // Each frame's instant is BETTER (higher) than the last because the
    // earlier-arriving ones look more delayed; only the LAST burst frame
    // approaches the truth. Earlier frames have lower instants and must
    // be ignored.
    observeServerTime(c, 10_050, 5_000);  // instant = 5050
    observeServerTime(c, 10_100, 5_000);  // instant = 5100
    observeServerTime(c, 10_150, 5_000);  // instant = 5150
    observeServerTime(c, 10_200, 5_000);  // instant = 5200
    observeServerTime(c, 10_250, 5_000);  // instant = 5250
    observeServerTime(c, 10_300, 5_000);  // instant = 5300 — max so far

    // The burst's tail frame represents the true offset (least delayed).
    // Max-instant tracking locks onto it.
    expect(c.offsetMs).toBe(5_300);

    // Subsequent clean frames at the true cadence (instant ~ 5300)
    // should not regress the offset.
    observeServerTime(c, 10_350, 5_050);  // instant = 5300
    observeServerTime(c, 10_400, 5_100);  // instant = 5300
    expect(c.offsetMs).toBe(5_300);
  });

  test("estimatedServerNow = clientNow + offset", () => {
    const c = newClockSync();
    observeServerTime(c, 10_000, 5_000);
    expect(estimatedServerNow(c, 6_000)).toBe(11_000);
  });

  test("ratchets up immediately to a better observation", () => {
    const c = newClockSync();
    observeServerTime(c, 0, 0); // offset = 0
    // Feed observations with a true offset of 1000. Max ratchets there
    // on the first such observation; the rest just confirm.
    observeServerTime(c, 1_000, 0);
    expect(c.offsetMs).toBe(1_000);
    for (let i = 1; i <= 20; i++) {
      observeServerTime(c, 1_000 + i, i); // instant = 1000 every time
    }
    expect(c.offsetMs).toBe(1_000);
  });

  test("sliding window lets a stale max age out so offset can drop", () => {
    const c = newClockSync();
    // One high-water observation, then fill the window with lower ones.
    observeServerTime(c, 10_000, 0);  // instant = 10000 (the high water)
    // Pump 40 lower observations to age the high-water out of the window.
    for (let i = 1; i <= 40; i++) {
      observeServerTime(c, 9_000 + i, i); // instant = 9000 each
    }
    // After 40 lower frames, the original 10000 has been evicted, so
    // the max within the window is now 9000.
    expect(c.offsetMs).toBe(9_000);
  });
});
