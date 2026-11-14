import type { ReactNode } from "react";
import { components } from "@provider/components";
import type { CredentialView } from "../../api/types";
import type { T } from "../../i18n";
import { credState, credSubline, displayName, isDirect, type QuotaEntry } from "../../lib/credentials";
import { formatTime } from "../../lib/format";
import { store } from "../../state/store";

// Four facts laid out like the usage figures; details are in the tooltip.
function Fact({ label, text, tip, extra, flag }: { label: string; text: string; tip?: string; extra?: ReactNode; flag?: string }) {
  return (
    <div className="meter">
      <div className="meter-label" title={label}>{label}{flag ? <span className="warn-text">{" · " + flag}</span> : null}</div>
      <div className="meter-value text" title={tip || text}>{text}{extra || null}</div>
    </div>
  );
}

function Facts({ t, cred }: { t: T; cred: CredentialView }) {
  const models = Array.isArray(cred.models) ? cred.models : [];
  const missing = Array.isArray(cred.missing_models) ? cred.missing_models : [];
  const modelsText = cred.all_models ? t("models.all") : t("models.selected", { count: models.length });
  const prefixText = cred.effective_prefix || t("prefix.none");
  let proxyText = t("proxy.inherit");
  let proxyTip: string | undefined;
  if (cred.proxy_name) {
    proxyText = cred.proxy_name;
    proxyTip = cred.proxy_url;
  } else if (cred.proxy_url) {
    proxyText = isDirect(cred.proxy_url) ? t("proxy.direct") : cred.proxy_url;
  }
  const ok = cred.success || 0;
  const failed = cred.failed || 0;
  return (
    <div className="facts">
      <Fact
        label={t("card.models")}
        text={modelsText}
        tip={[
          modelsText,
          components.modelsTip ? components.modelsTip(t, cred) : "",
          cred.registered_known ? t("models.registered", { count: cred.registered_models }) : "",
          missing.length ? t("models.missing", { list: missing.join(", ") }) : "",
        ].filter(Boolean).join("\n")}
        flag={missing.length ? t("models.missingCount", { count: missing.length }) : ""}
      />
      <Fact
        label={t("card.prefix")}
        text={prefixText}
        tip={prefixText + "\n" + t(cred.prefix === null || cred.prefix === undefined ? "prefix.default" : "prefix.custom")}
      />
      <Fact label={t("card.proxy")} text={proxyText} tip={proxyTip} />
      <Fact
        label={t("card.requestsLabel")}
        text={ok + " / "}
        tip={t("card.requests", { ok, failed })}
        extra={<span className={failed ? "danger-text" : ""}>{String(failed)}</span>}
      />
    </div>
  );
}

/**
 * A key's card: name, state and file, the facts, the provider's figures
 * (components.KeyUsage), notes, and the actions. armedDelete is the name a
 * two-click action is armed for (State.armedDelete).
 */
export function CredCard({ t, cred, q, armedDelete, busy, locale }: {
  t: T;
  cred: CredentialView;
  q: QuotaEntry | undefined;
  armedDelete: string | null;
  busy: Record<string, true>;
  locale: string;
}) {
  const [badgeClass, badgeKey] = credState(cred);
  // Why the key is off or cooling down, under the figures on its own line.
  const stateNote = cred.read_error || cred.status_message || "";
  const name = displayName(cred);
  const armed = armedDelete === cred.name;
  const [corner, cornerNote] = components.keyCorner ? components.keyCorner(t, cred, q) : [null, null];
  const { KeyUsage, KeyActions } = components;
  return (
    <article className={"cred" + (cred.disabled ? " off" : "")}>
      <div className="cred-head">
        <div className="cred-row">
          <div className="cred-title">
            <span className="cred-name" title={name}>{name}</span>
            <span className={"badge " + badgeClass}>{t(badgeKey)}</span>
          </div>
          {corner}
        </div>
        <div className="cred-row">
          <div className="cred-file" title={cred.name}>{credSubline(cred, t("card.priority"))}</div>
          {cornerNote}
        </div>
      </div>
      <div className="cred-body">
        {cred.read_error ? null : <Facts t={t} cred={cred} />}
        <KeyUsage t={t} cred={cred} q={q} locale={locale} />
        {cred.note ? <div className="usage-note sub" title={cred.note}>{t("card.noteLine", { msg: cred.note })}</div> : null}
        {stateNote ? <div className={"usage-note " + (cred.read_error ? "danger-text" : "warn-text")}>{t("card.statusNote", { msg: stateNote })}</div> : null}
      </div>
      <div className="cred-foot">
        <div className="cred-meta">
          {q && q.data && q.data.fetched_at ? (
            <span title={components.fetchedTip ? components.fetchedTip(t, q.data, locale) : undefined}>
              {t("usage.fetched", { time: formatTime(q.data.fetched_at, locale) })}
            </span>
          ) : null}
          {!cred.read_error && q && q.data ? (
            <button type="button" className="btn-link" disabled={q.state === "loading"} onClick={() => void store.loadQuota(cred.auth_index)}>
              {t(q.state === "loading" ? "usage.loading" : "usage.refresh")}
            </button>
          ) : null}
        </div>
        <div className="row-actions">
          {KeyActions ? <KeyActions t={t} cred={cred} busy={busy} armedDelete={armedDelete} /> : null}
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!cred.read_error} onClick={() => store.openEditor("edit", cred)}>
            {t("row.edit")}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!busy["toggle:" + cred.name]} onClick={() => void store.toggleDisabled(cred)}>
            {t(cred.disabled ? "row.enable" : "row.disable")}
          </button>
          <button
            type="button"
            className={"btn btn-danger btn-sm" + (armed ? " armed" : "")}
            disabled={!!busy["delete:" + cred.name]}
            onClick={() => void store.deleteCredential(cred)}
          >
            {t(armed ? "row.confirmDelete" : "row.delete")}
          </button>
        </div>
      </div>
    </article>
  );
}
