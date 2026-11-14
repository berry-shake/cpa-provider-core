// What the shared page asks of a provider. A provider's web/src/provider/
// directory is reachable as "@provider/..." and must hold:
//
//   plugin.ts       export const plugin: ProviderPlugin
//   types.ts        the JSON shapes and fields that differ (listed below)
//   components.tsx  export const components: ProviderComponents
//   i18n/en.json, i18n/zh.json   messages added to (or replacing) the shared ones
//   styles.css      rules appended after the shared styles
//
// plugin.ts is read while the shared modules load: keep it to constants and
// functions, and import shared code there only for use inside functions.
// See web/README.md.

import type { ComponentType, ReactNode } from "react";
import type { EditorExtra } from "@provider/types";
import type { CatalogMeta, CredentialSaveRequest, CredentialView, ModelView, QuotaView } from "./api/types";
import type { Params, T } from "./i18n";
import type { QuotaEntry, QuotaMap } from "./lib/credentials";
import type { EditorState } from "./lib/editor";
import type { State } from "./state/store";

// @provider/types exports these types (checked where they are used):
//
//   ModelView            a catalog model (needs id: string)
//   QuotaView            what /credentials/quota answers (needs fetched_at: string)
//   CredentialExtra      fields CredentialView adds ({} for none)
//   CatalogExtra         fields CatalogMeta adds
//   CredentialSaveExtra  fields the credential save body adds
//   EditorExtra          fields EditorState adds, set by plugin.editor.init

export interface ProviderPlugin {
  /** Plugin ID: management and resource paths, storage keys, the config section, the default prefix. */
  id: string;
  /** Display name, e.g. "Wokey". */
  name: string;
  /** Source repository, linked from the footer. */
  repo: string;
  /** Token counts of a billion and more read "1.2B" instead of "1200M". */
  billionTokens?: boolean;
  /**
   * The keys whose quota to load (see quotaGroups in lib/credentials): the
   * page loads the keys of a group one after another and the groups side by
   * side. Default: one group per egress.
   */
  quotaGroups?(creds: CredentialView[], quota: QuotaMap, force: boolean): string[][];
  /** Reloading all quotas asks the plugin to refetch (&refresh=1). */
  quotaRefresh?: boolean;
  models: {
    /** Whether a model matches a search besides by ID; filter is trimmed, lower-cased and not empty. */
    matches(m: ModelView, filter: string): boolean;
    /** Whether "all models" on a key covers the model. Default: every model. */
    inAll?(m: ModelView, cred: CredentialView): boolean;
    /** Extra params for catalog.summary and notes after the catalog hint. */
    hint?(t: T, meta: CatalogMeta): { params?: Params; notes?: string[] };
  };
  editor: {
    /** The provider's fields of a new form; cred is the key being edited, null when adding. */
    init(cred: CredentialView | null): EditorExtra;
    /**
     * Puts the provider's fields into a save body (called after priority);
     * returns a message key to refuse the form.
     */
    build?(ed: EditorState, body: CredentialSaveRequest, adding: boolean): string | undefined;
    /** Secret fields to blank while the closed dialog keeps the last form; null when there are none. */
    clearSecrets?(ed: EditorState): Partial<EditorState> | null;
    /** Whether saving an edit reloads the key's quota (adding always does). */
    reloadsQuota?(body: CredentialSaveRequest): boolean;
    /** The models the picker offers. Default: all. */
    pickable?(models: ModelView[], ed: EditorState): ModelView[];
    /** Tooltip of the picker's count. */
    countTip?(t: T, ed: EditorState): string | undefined;
    /** The name column of a picker row and its tooltip. */
    pickName(t: T, m: ModelView): { text: string; title: string };
    /** The details column of a picker row, e.g. "200K context · $3 / $15". */
    modelMeta(t: T, m: ModelView): string;
  };
}

export interface KeyCardProps {
  t: T;
  cred: CredentialView;
  q: QuotaEntry | undefined;
  locale: string;
}

export interface ProviderComponents {
  /** The strip above the key cards; creds holds at least one key. */
  KeySummary: ComponentType<{ t: T; s: State; creds: CredentialView[] }>;
  /** A card's figures under the facts (or why there are none), with their bars and notes. */
  KeyUsage: ComponentType<KeyCardProps>;
  /** Right-hand ends of a card's name row and file row. */
  keyCorner?(t: T, cred: CredentialView, q: QuotaEntry | undefined): [ReactNode, ReactNode];
  /** An extra line for the tooltip of a card's models fact; "" for none. */
  modelsTip?(t: T, cred: CredentialView): string;
  /** Tooltip of a card's "fetched" time. */
  fetchedTip?(t: T, d: QuotaView, locale: string): string;
  /** Buttons before Edit on a card; armedDelete is State.armedDelete. */
  KeyActions?: ComponentType<{ t: T; cred: CredentialView; busy: Record<string, true>; armedDelete: string | null }>;
  /** Header message keys of the catalog cells ModelCells draws; the CPA-name column follows. */
  modelColumns: string[];
  /** The catalog row's cells before the CPA names, the model ID cell included. */
  ModelCells: ComponentType<{ t: T; m: ModelView; creds: CredentialView[] }>;
  /** Fields of the key editor after priority. */
  EditorFields?: ComponentType<{ t: T; s: State; ed: EditorState }>;
  /** Dialogs mounted after the proxy dialog. */
  Dialogs?: ComponentType<{ t: T; s: State }>;
}
