import { plugin } from "@provider/plugin";
import type { CredentialView, ModelView, StatusView } from "../api/types";
import {
  allCredentials,
  barView,
  credentialsOf,
  credMatches,
  credState,
  credSubline,
  defaultPrefix,
  displayName,
  filterModels,
  forceState,
  isDirect,
  modelUsage,
  egressKey,
  quotaGroups,
  statusHealth,
  type QuotaMap,
} from "./credentials";

function cred(over: Partial<CredentialView> = {}): CredentialView {
  return {
    auth_index: "1",
    id: "wokey-aaaa.json",
    name: "wokey-aaaa.json",
    label: "",
    key_masked: "sk-wk-****aaaa",
    disabled: false,
    unavailable: false,
    priority: 0,
    prefix: null,
    effective_prefix: "wokey",
    all_models: true,
    models: null,
    registered_models: 0,
    registered_known: false,
    success: 0,
    failed: 0,
    ...over,
  };
}

function status(over: Partial<StatusView> = {}): StatusView {
  return {
    plugin: "wokey",
    version: "0.1.8",
    now: "2026-11-08T10:00:00Z",
    config: {
      base_url: "https://api.wokey.ai",
      default_prefix: "wokey",
      models_refresh_minutes: 60,
      request_timeout_seconds: 300,
      stream_idle_timeout_seconds: 120,
    },
    catalog: { count: 12 },
    credentials: [cred()],
    ...over,
  };
}

const model = (id: string, over: Partial<ModelView> = {}): ModelView => ({ id, ...over });

describe("display rules", () => {
  it("names a key by label, then file stem, then masked key", () => {
    expect(displayName(cred({ label: "Team A" }))).toBe("Team A");
    expect(displayName(cred())).toBe("wokey-aaaa");
    expect(displayName(cred({ name: "" }))).toBe("sk-wk-****aaaa");
  });

  it("shows the file name under a custom label only", () => {
    expect(credSubline(cred(), "Priority")).toBe("sk-wk-****aaaa");
    expect(credSubline(cred({ label: "wokey-aaaa" }), "Priority")).toBe("sk-wk-****aaaa");
    expect(credSubline(cred({ label: "Team A", priority: 5 }), "Priority")).toBe("wokey-aaaa · sk-wk-****aaaa · Priority 5");
  });

  it("maps state to badge class and message", () => {
    expect(credState(cred({ read_error: "bad json", disabled: true }))).toEqual(["danger", "state.readError"]);
    expect(credState(cred({ disabled: true, unavailable: true }))).toEqual(["", "state.disabled"]);
    expect(credState(cred({ unavailable: true }))).toEqual(["warn", "state.unavailable"]);
    expect(credState(cred())).toEqual(["ok", "state.enabled"]);
  });

  it("recognises direct proxies", () => {
    expect(isDirect(" Direct ")).toBe(true);
    expect(isDirect("none")).toBe(true);
    expect(isDirect("socks5://h:1080")).toBe(false);
    expect(isDirect(undefined)).toBe(false);
  });

  it("filters keys by label, masked key, file, prefix and note", () => {
    const c = cred({ label: "Team A", note: "cooling down" });
    expect(credMatches(c, "")).toBe(true);
    expect(credMatches(c, "team")).toBe(true);
    expect(credMatches(c, "aaaa")).toBe(true);
    expect(credMatches(c, "wokey")).toBe(true);
    expect(credMatches(c, "cooling")).toBe(true);
    expect(credMatches(c, "zzz")).toBe(false);
  });
});

describe("status helpers", () => {
  it("hides credentials when listing failed, except for log lookups", () => {
    const failed = status({ list_error: "boom" });
    expect(credentialsOf(failed)).toEqual([]);
    expect(allCredentials(failed)).toHaveLength(1);
    expect(credentialsOf(null)).toEqual([]);
    expect(allCredentials(null)).toEqual([]);
  });

  it("uses the plugin ID as the default prefix until the status arrives", () => {
    expect(defaultPrefix(null)).toBe("demo");
    expect(defaultPrefix(status())).toBe("wokey");
    expect(defaultPrefix(status({ config: { ...status().config, default_prefix: "" } }))).toBe("");
  });

  it("derives the health badge", () => {
    expect(statusHealth(status({ list_error: "x" }))).toEqual({ state: "error", key: "health.error", params: null });
    expect(statusHealth(status({ catalog: { count: 0 } }))).toEqual({ state: "warn", key: "health.catalog", params: null });
    expect(statusHealth(status({ credentials: [] }))).toEqual({ state: "warn", key: "health.noKeys", params: null });
    expect(statusHealth(status({ credentials: [cred(), cred({ auth_index: "2", disabled: true })] }))).toEqual({
      state: "ok",
      key: "health.ok",
      params: { enabled: 1, total: 2 },
    });
  });

  it("reads force-model-prefix", () => {
    expect(forceState({ force_model_prefix: true })).toBe("On");
    expect(forceState({ force_model_prefix: false })).toBe("Off");
    expect(forceState({})).toBe("Unknown");
    expect(forceState(null)).toBe("Unknown");
  });
});

describe("quotaGroups", () => {
  const creds = [
    cred({ auth_index: "1" }),
    cred({ auth_index: "2", disabled: true, proxy_url: "socks5h://u:***@p.example:1080" }),
    cred({ auth_index: "3", disabled: true }),
    cred({ auth_index: "4", read_error: "bad" }),
    cred({ auth_index: "5", proxy_url: "direct" }),
    cred({ auth_index: "6", proxy_url: "socks5h://u:***@p.example:1080" }),
    cred({ auth_index: "7", proxy_url: "NONE" }),
  ];
  const quota: QuotaMap = { "3": { state: "ok" }, "5": { state: "ok" } };

  it("loads keys with no balance yet, disabled ones too, grouped by egress", () => {
    expect(quotaGroups(creds, quota, false)).toEqual([["1"], ["2", "6"], ["7"]]);
  });

  it("with force reloads every readable key", () => {
    expect(quotaGroups(creds, quota, true)).toEqual([["1", "3"], ["2", "6"], ["5", "7"]]);
  });

  it("lets the provider group the keys its own way", () => {
    plugin.quotaGroups = (cs, q, force) => cs.filter((c) => force || !q[c.auth_index]).map((c) => [c.auth_index]);
    try {
      expect(quotaGroups(creds.slice(0, 3), quota, false)).toEqual([["1"], ["2"]]);
    } finally {
      delete plugin.quotaGroups;
    }
  });

  it("names the egress a proxy setting picks", () => {
    expect([egressKey(undefined), egressKey(" "), egressKey("Direct"), egressKey("none"), egressKey(" http://h:1 ")])
      .toEqual(["inherit", "inherit", "direct", "direct", "http://h:1"]);
  });
});

describe("models", () => {
  const models = [model("gpt-5", { name: "GPT-5", owned_by: "openai" }), model("claude-sonnet-4-5", { owned_by: "anthropic" }), model("glm-4.6")];

  it("lists the names each model is registered under and how many keys serve it", () => {
    const usage = modelUsage(
      [
        cred({ auth_index: "1", all_models: true, effective_prefix: "wokey" }),
        cred({ auth_index: "2", all_models: false, models: ["gpt-5"], effective_prefix: "" }),
        cred({ auth_index: "3", all_models: false, models: ["gpt-5"], effective_prefix: "wokey" }),
        cred({ auth_index: "4", all_models: true, disabled: true }),
        cred({ auth_index: "5", all_models: true, read_error: "x" }),
      ],
      models,
    );
    expect(usage.get("gpt-5")).toEqual({ keys: 3, names: new Set(["wokey/gpt-5", "gpt-5"]) });
    expect(usage.get("glm-4.6")).toEqual({ keys: 1, names: new Set(["wokey/glm-4.6"]) });
    expect(usage.has("missing")).toBe(false);
  });

  it("lets the provider narrow what all models covers", () => {
    plugin.models.inAll = (m) => m.id !== "glm-4.6";
    try {
      const usage = modelUsage([cred({ all_models: true, effective_prefix: "" })], models);
      expect([...usage.keys()]).toEqual(["gpt-5", "claude-sonnet-4-5"]);
    } finally {
      delete plugin.models.inAll;
    }
  });

  it("filters by id and what the provider matches (name and owner here)", () => {
    expect(filterModels(models, "").map((m) => m.id)).toEqual(["gpt-5", "claude-sonnet-4-5", "glm-4.6"]);
    expect(filterModels(models, "  GPT ").map((m) => m.id)).toEqual(["gpt-5"]);
    expect(filterModels(models, "anthropic").map((m) => m.id)).toEqual(["claude-sonnet-4-5"]);
    expect(filterModels(models, "nothing")).toEqual([]);
  });
});

describe("barView", () => {
  it("computes the share left and its level", () => {
    expect(barView(63.7, 100)).toMatchObject({ level: "", width: "63.7%" });
    expect(barView(29.99, 100).level).toBe(" warn");
    expect(barView(9.9, 100).level).toBe(" bad");
    expect(barView(25, 10)).toMatchObject({ fraction: 1, width: "100.0%" });
    expect(barView(5, 0)).toMatchObject({ fraction: 0, width: "0.0%", level: " bad" });
  });
});
