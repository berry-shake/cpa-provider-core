// Building blocks for the provider's parts of the key cards and the summary
// strip (components.KeySummary, components.KeyUsage).

import type { CredentialView } from "../../api/types";
import type { T } from "../../i18n";
import { barView, type QuotaEntry } from "../../lib/credentials";
import { store } from "../../state/store";

/** One cell of a stat strip: label above, value with an optional note beside it; tip adds a tooltip line. */
export function StatItem({ label, value, note, tip }: { label: string; value: string; note?: string; tip?: string }) {
  return (
    <div className="strip-item">
      <div className="strip-label" title={tip ? label + "\n" + tip : label}>{label}</div>
      <div className="strip-line">
        <span className="strip-value" title={tip || undefined}>{value}</span>
        {note ? <span className="strip-note" title={note}>{note}</span> : null}
      </div>
    </div>
  );
}

/**
 * One figure of a card's usage grid: label over value, an optional note
 * below; tip goes into both tooltips after sep.
 */
export function Meter({ label, value, note, noteClass, tip, sep = " · " }: {
  label: string;
  value: string;
  note?: string;
  noteClass?: string;
  tip?: string;
  sep?: string;
}) {
  return (
    <div className="meter">
      <div className="meter-label" title={tip ? label + sep + tip : label}>{label}</div>
      <div className="meter-value" title={tip ? value + sep + tip : value}>{value}</div>
      {note ? <div className={"meter-note" + (noteClass ? " " + noteClass : "")} title={note}>{note}</div> : null}
    </div>
  );
}

/**
 * A remaining-allowance bar: what it is (and when it resets) on the left,
 * what is left on the right, coloured like the bar.
 */
export function Bar({ t, label, left, total, value }: { t: T; label: string; left: number; total: number; value: string }) {
  const bar = barView(left, total);
  return (
    <div className={"cap" + bar.level}>
      <div className="cap-head">
        <span className="cap-label" title={label}>{label}</span>
        <span className="cap-left">{t("usage.left")} <strong>{value}</strong></span>
      </div>
      <div className="track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(bar.fraction * 100)}>
        <span className="fill" style={{ width: bar.width }}></span>
      </div>
    </div>
  );
}

/** What a card shows before its figures arrive: loading, the error with a retry, or a check button. */
export function UsagePending({ t, cred, q }: { t: T; cred: CredentialView; q: QuotaEntry | undefined }) {
  const load = () => void store.loadQuota(cred.auth_index);
  return (
    <div className="usage-state">
      {q && q.state === "loading" ? (
        <span>{t("usage.loading")}</span>
      ) : q && q.state === "error" ? (
        <>
          <span className="danger-text">{t("usage.failed", { msg: q.error })}</span>
          <button type="button" className="btn btn-secondary btn-sm" onClick={load}>{t("usage.retry")}</button>
        </>
      ) : (
        <>
          <span>{t(cred.disabled ? "usage.disabled" : "usage.notLoaded")}</span>
          <button type="button" className="btn btn-secondary btn-sm" onClick={load}>{t("usage.check")}</button>
        </>
      )}
    </div>
  );
}
