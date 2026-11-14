import { components } from "@provider/components";
import { localeTag, type T } from "../../i18n";
import { credentialsOf, credMatches } from "../../lib/credentials";
import { store, type State } from "../../state/store";
import { CredCard } from "./CredCard";

// The provider's totals over the keys (components.KeySummary); hidden while there are none.
function KeySummary({ t, s }: { t: T; s: State }) {
  const creds = credentialsOf(s.status);
  if (!creds.length) return <div className="summary strip" id="key-summary" hidden></div>;
  const Summary = components.KeySummary;
  return <Summary t={t} s={s} creds={creds} />;
}

function KeysBody({ t, s }: { t: T; s: State }) {
  const data = s.status;
  const creds = credentialsOf(data);
  if (!data) return <div className="empty">{t("keys.notLoaded")}</div>;
  if (data.list_error) return <div className="empty danger-text">{t("keys.listError", { msg: data.list_error })}</div>;
  if (creds.length === 0) {
    return (
      <div className="empty-state">
        <strong>{t("keys.emptyTitle")}</strong>
        <div>{t("keys.empty")}</div>
        <button type="button" className="btn btn-primary" onClick={() => store.openEditor("add")}>{t("actions.add")}</button>
      </div>
    );
  }
  const filter = s.keysFilter.trim().toLowerCase();
  const visible = creds.filter((c) => credMatches(c, filter));
  if (visible.length === 0) return <div className="empty">{t("keys.noMatch")}</div>;
  const locale = localeTag(s.lang);
  return (
    <>
      {visible.map((cred) => (
        <CredCard
          key={cred.auth_index || cred.name}
          t={t}
          cred={cred}
          q={s.quota[cred.auth_index]}
          armedDelete={s.armedDelete}
          busy={s.busy}
          locale={locale}
        />
      ))}
    </>
  );
}

export function KeysPanel({ t, s }: { t: T; s: State }) {
  const data = s.status;
  const creds = credentialsOf(data);
  const filter = s.keysFilter.trim().toLowerCase();
  let count = "";
  if (creds.length) {
    const shown = creds.filter((c) => credMatches(c, filter)).length;
    count = filter ? t("keys.shown", { shown, total: creds.length }) : t("keys.count", { count: creds.length });
  }
  return (
    <section className="panel" role="tabpanel" id="panel-keys" aria-labelledby="tab-keys" hidden={s.tab !== "keys"}>
      <KeySummary t={t} s={s} />
      <section className="card">
        <div className="card-header">
          <div className="card-title">
            <h2>{t("keys.title")}</h2>
            <span className="hint">{t("keys.hint")}</span>
          </div>
          <div className="actions">
            <button id="quota-button" className="btn btn-secondary btn-sm" type="button" hidden={creds.length === 0} disabled={!!s.busy.quota} onClick={() => void store.refreshQuotas()}>
              {t("actions.reloadQuota")}
            </button>
            {/* The empty state carries its own add button. */}
            <button id="add-button" className="btn btn-primary btn-sm" type="button" hidden={!!data && !data.list_error && creds.length === 0} onClick={() => store.openEditor("add")}>
              {t("actions.add")}
            </button>
          </div>
        </div>
        <div className="toolbar" id="keys-toolbar" hidden={creds.length === 0}>
          <input
            className="input input-sm search"
            id="keys-search"
            type="search"
            spellCheck={false}
            placeholder={t("keys.search")}
            value={s.keysFilter}
            onChange={(event) => store.setKeysFilter(event.target.value)}
          />
          <span className="count" id="keys-count">{count}</span>
        </div>
        <div className="cred-list" id="keys-body">
          <KeysBody t={t} s={s} />
        </div>
      </section>
    </section>
  );
}
