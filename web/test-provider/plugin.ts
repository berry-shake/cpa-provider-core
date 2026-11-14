import type { ProviderPlugin } from "@core/contract";
import { fmtTokens } from "@core/lib/format";

export const plugin: ProviderPlugin = {
  id: "demo",
  name: "Demo",
  repo: "https://github.com/berry-shake/cpa-demo-plugin",
  models: {
    matches: (m, filter) =>
      String(m.name || "").toLowerCase().includes(filter) ||
      String(m.owned_by || "").toLowerCase().includes(filter),
    hint: (_t, meta) => ({ params: { keys: meta.keys || 0 } }),
  },
  editor: {
    init: () => ({ token: "" }),
    build(ed, body) {
      const token = ed.token.trim();
      if (!token) return undefined;
      if (!/^tk_\S+$/.test(token)) return "form.badToken";
      body.token = token;
      return undefined;
    },
    clearSecrets: (ed) => (ed.token ? { token: "" } : null),
    reloadsQuota: (body) => body.token !== undefined,
    pickName: (_t, m) => ({ text: m.name || "", title: m.name || "" }),
    modelMeta: (t, m) => (m.context_length ? t("picker.ctx", { ctx: fmtTokens(m.context_length) }) : ""),
  },
};
