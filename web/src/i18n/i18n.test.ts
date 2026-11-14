import providerEn from "@provider/i18n/en.json";
import en from "./en.json";
import zh from "./zh.json";
import { detectDefaultLang, htmlLang, isLang, LANG_KEY, localeTag, makeT, messages, translate } from "./index";

describe("messages", () => {
  const names = (text: string) => (text.match(/\{\w+\}/g) || []).sort();

  it("has the same keys in both languages", () => {
    expect(Object.keys(zh).sort()).toEqual(Object.keys(en).sort());
    expect(Object.keys(messages.zh).sort()).toEqual(Object.keys(messages.en).sort());
  });

  it("uses the same placeholders in both languages", () => {
    for (const key of Object.keys(messages.en)) expect([key, names(messages.zh[key])]).toEqual([key, names(messages.en[key])]);
  });

  it("adds the provider's messages to the shared ones", () => {
    expect(messages.en["login.title"]).toBe(en["login.title"]);
    expect(messages.en.title).toBe(providerEn.title);
    expect(Object.keys(messages.en)).toHaveLength(new Set([...Object.keys(en), ...Object.keys(providerEn)]).size);
    expect(LANG_KEY).toBe("cliproxy-demo-lang");
  });
});

describe("detectDefaultLang", () => {
  it("takes the first zh or en tag", () => {
    expect(detectDefaultLang(["zh-CN", "en"])).toBe("zh");
    expect(detectDefaultLang(["de-DE", "en-US", "zh"])).toBe("en");
    expect(detectDefaultLang(["fr", "zh-TW"])).toBe("zh");
    expect(detectDefaultLang(["fr"])).toBe("en");
  });

  it("falls back to navigator.language", () => {
    expect(detectDefaultLang([], "zh-HK")).toBe("zh");
    expect(detectDefaultLang(undefined, undefined)).toBe("en");
  });
});

describe("translate", () => {
  it("fills placeholders that have values", () => {
    expect(translate("en", "health.ok", { enabled: 2, total: 3 })).toBe("2/3 keys enabled");
    expect(translate("en", "health.ok", { enabled: 2 })).toBe("2/{total} keys enabled");
  });

  it("falls back to English, then the key", () => {
    expect(translate("zh", "no.such.key")).toBe("no.such.key");
    expect(makeT("zh")("title")).toBe(messages.zh.title);
  });

  it("maps languages to tags", () => {
    expect(localeTag("zh")).toBe("zh-CN");
    expect(localeTag("en")).toBe("en-US");
    expect(htmlLang("zh")).toBe("zh-CN");
    expect(htmlLang("en")).toBe("en");
    expect(isLang("zh")).toBe(true);
    expect(isLang("de")).toBe(false);
  });
});
