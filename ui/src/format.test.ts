import { describe, expect, it } from "vitest";
import { formatCost, formatTokens, relativeStamp, sessionGroup } from "./format";

describe("formatTokens", () => {
  it("renders zero as a dash and thousands as k", () => {
    expect(formatTokens(0)).toBe("—");
    expect(formatTokens(12)).toBe("12");
    expect(formatTokens(1234)).toBe("1.2k");
  });
});

describe("formatCost", () => {
  it("keeps four decimals and marks sub-cent costs", () => {
    expect(formatCost(0)).toBe("$0");
    expect(formatCost(0.00001)).toBe("<$0.0001");
    expect(formatCost(0.012345)).toBe("$0.0123");
  });
});

describe("session grouping", () => {
  const today = new Date(2026, 10, 19, 12, 0, 0);

  it("buckets local MM-DD stamps into today, yesterday and earlier", () => {
    expect(sessionGroup("11-19 09:00", today)).toBe("今天");
    expect(sessionGroup("11-18 09:00", today)).toBe("昨天");
    expect(sessionGroup("10-01 09:00", today)).toBe("更早");
    expect(sessionGroup("garbage", today)).toBe("更早");
  });

  it("uses a short relative label for recent sessions", () => {
    expect(relativeStamp("11-19 09:00", today)).toBe("今天 09:00");
    expect(relativeStamp("10-01 09:00", today)).toBe("10-01 09:00");
  });
});
