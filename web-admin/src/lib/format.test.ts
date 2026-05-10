import { describe, it, expect } from "vitest";
import { fmtBytes, fmtDuration, fmtLoad, fmtUsAsMs } from "./format";

describe("format", () => {
  it("fmtBytes scales", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(1023)).toBe("1023 B");
    expect(fmtBytes(1024)).toBe("1.00 KB");
    expect(fmtBytes(1024 * 1024 * 5)).toBe("5.00 MB");
  });

  it("fmtDuration", () => {
    expect(fmtDuration(0)).toBe("0ms");
    expect(fmtDuration(800)).toBe("800ms");
    expect(fmtDuration(2_500)).toBe("2.5s");
    expect(fmtDuration(120_000)).toBe("2m");
  });

  it("fmtLoad clamps and percentizes", () => {
    expect(fmtLoad(0)).toBe("0%");
    expect(fmtLoad(0.42)).toBe("42%");
    expect(fmtLoad(1.5)).toBe("150%");
  });

  it("fmtUsAsMs", () => {
    expect(fmtUsAsMs(0)).toBe("0.0ms");
    expect(fmtUsAsMs(1500)).toBe("1.5ms");
    expect(fmtUsAsMs(50_000)).toBe("50.0ms");
  });
});
