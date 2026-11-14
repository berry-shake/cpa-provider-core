import { Meter, StatItem, UsagePending } from "@core/components/keys/parts";
import type { KeyCardProps, ProviderComponents } from "@core/contract";
import type { CredentialView } from "@core/api/types";
import type { T } from "@core/i18n";
import { fmtTokens, fmtUSD } from "@core/lib/format";
import type { ModelView } from "./types";

function KeySummary({ t, creds }: { t: T; creds: CredentialView[] }) {
  return (
    <div className="summary strip" id="key-summary">
      <StatItem label={t("summary.keys")} value={String(creds.length)} />
    </div>
  );
}

function KeyUsage({ t, cred, q }: KeyCardProps) {
  if (cred.read_error) return null;
  const d = q && q.data;
  if (!d) return <UsagePending t={t} cred={cred} q={q} />;
  return (
    <div className="meters">
      <Meter label={t("usage.balance")} value={d.balance_usd !== undefined ? fmtUSD(d.balance_usd) : "—"} note={d.balance_error} noteClass="danger-text" />
    </div>
  );
}

function ModelCells({ m }: { m: ModelView }) {
  return (
    <>
      <td>
        <div className="mono strong">{m.id}</div>
        {m.name && m.name !== m.id ? <div className="sub">{m.name}</div> : null}
      </td>
      <td className="nowrap">{m.owned_by || "—"}</td>
      <td className="nowrap">{m.context_length ? fmtTokens(m.context_length) : "—"}</td>
    </>
  );
}

export const components: ProviderComponents = {
  KeySummary,
  KeyUsage,
  modelColumns: ["col.model", "col.owner", "col.context"],
  ModelCells,
};
