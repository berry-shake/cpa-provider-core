import { plugin } from "@provider/plugin";
import { cleanPrefix, fileStem, fmtCount, fmtMS, fmtPct, fmtRate, fmtTokens, fmtUSD, formatShort, formatTime, formatWhen, yamlScalar } from "./format";

describe("fmtUSD", () => {
  it.each([
    [0, "$0"],
    [1234.56, "$1235"],
    [100, "$100"],
    [12.345, "$12.35"],
    [0.01, "$0.01"],
    [0.00456, "$0.0046"],
    [-3.5, "$-3.50"],
    ["7", "$7.00"],
  ])("%s -> %s", (value, want) => {
    expect(fmtUSD(value)).toBe(want);
  });

  it("shows a dash for values that are not numbers", () => {
    expect(fmtUSD(undefined)).toBe("—");
    expect(fmtUSD("abc")).toBe("—");
    expect(fmtUSD(Infinity)).toBe("—");
  });
});

describe("fmtTokens", () => {
  it.each([
    [0, "0"],
    [999, "999"],
    [1000, "1K"],
    [48211, "48K"],
    [1000000, "1M"],
    [1250000, "1.3M"],
    [undefined, "0"],
  ])("%s -> %s", (value, want) => {
    expect(fmtTokens(value)).toBe(want);
  });

  it("writes billions only for providers that ask", () => {
    expect(fmtTokens(1500000000)).toBe("1500M");
    plugin.billionTokens = true;
    try {
      expect(fmtTokens(1500000000)).toBe("1.5B");
      expect(fmtTokens(999000000)).toBe("999M");
    } finally {
      delete plugin.billionTokens;
    }
  });
});

describe("fmtMS", () => {
  it.each([
    [undefined, "—"],
    [0, "—"],
    [-5, "—"],
    [612, "0.6s"],
    [8421, "8.4s"],
    [59999, "60.0s"],
    [60000, "1m 0s"],
    [125400, "2m 5s"],
  ])("%s -> %s", (value, want) => {
    expect(fmtMS(value as number | undefined)).toBe(want);
  });
});

describe("rates, counts and shares", () => {
  it("formats output speed", () => {
    expect(fmtRate(undefined)).toBe("—");
    expect(fmtRate(null)).toBe("—");
    expect(fmtRate(259.2)).toBe("259 t/s");
    expect(fmtRate(0)).toBe("0 t/s");
  });

  it("formats counts with the default locale", () => {
    expect(fmtCount(undefined)).toBe("0");
    expect(fmtCount(42)).toBe((42).toLocaleString());
    expect(fmtCount(12345)).toBe((12345).toLocaleString());
  });

  it("formats shares as percentages", () => {
    expect(fmtPct(undefined)).toBe("—");
    expect(fmtPct(0)).toBe("0.0%");
    expect(fmtPct(0.8496)).toBe("85.0%");
    expect(fmtPct(1)).toBe("100.0%");
  });
});

describe("dates", () => {
  it("formats timestamps in the given locale", () => {
    const iso = "2026-11-08T10:02:11Z";
    expect(formatWhen(iso, "en-US")).toBe(new Date(iso).toLocaleString("en-US"));
    expect(formatWhen(iso, "zh-CN")).toBe(new Date(iso).toLocaleString("zh-CN"));
    expect(formatTime(iso, "en-US")).toBe(new Date(iso).toLocaleTimeString("en-US"));
  });

  it("formats a short date without the year", () => {
    const iso = "2026-11-08T10:02:11Z";
    expect(formatShort(iso, "en-US")).toBe(new Date(iso).toLocaleString("en-US", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }));
    expect(formatShort(undefined, "en-US")).toBe("—");
    expect(formatShort("soon", "en-US")).toBe("soon");
  });

  it("keeps values it cannot parse", () => {
    expect(formatWhen("", "en-US")).toBe("—");
    expect(formatWhen(undefined, "en-US")).toBe("—");
    expect(formatWhen("soon", "en-US")).toBe("soon");
    expect(formatTime("soon", "en-US")).toBe("soon");
    expect(formatTime(undefined, "en-US")).toBe("");
  });
});

describe("fileStem", () => {
  it.each([
    ["wokey-ab12.json", "wokey-ab12"],
    ["dir/wokey-ab12.json", "wokey-ab12"],
    ["C:\\auth\\wokey.json", "wokey"],
    [".hidden", ".hidden"],
    ["noext", "noext"],
    ["  spaced.json ", "spaced"],
    ["", ""],
    [undefined, ""],
  ])("%s -> %s", (value, want) => {
    expect(fileStem(value)).toBe(want);
  });
});

describe("yamlScalar", () => {
  it.each([
    ["https://api.wokey.ai", "https://api.wokey.ai"],
    ["wokey", "wokey"],
    ["", '""'],
    ["-dash", '"-dash"'],
    ["a: b", '"a: b"'],
    ["a #b", '"a #b"'],
    ["trailing ", '"trailing "'],
    [" leading", '" leading"'],
    ["*star", '"*star"'],
  ])("%s -> %s", (value, want) => {
    expect(yamlScalar(value)).toBe(want);
  });
});

describe("cleanPrefix", () => {
  it("trims spaces and slashes", () => {
    expect(cleanPrefix(" /wokey/ ")).toBe("wokey");
    expect(cleanPrefix("a/b")).toBe("a/b");
    expect(cleanPrefix("///")).toBe("");
    expect(cleanPrefix(null)).toBe("");
    expect(cleanPrefix(undefined)).toBe("");
  });
});
