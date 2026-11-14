// A made-up provider for the core's own tests and typecheck: a model with a
// name and an owner, a balance per key, and one secret editor field.

export interface ModelView {
  id: string;
  name?: string;
  owned_by?: string;
  context_length?: number;
}

export interface QuotaView {
  balance_usd?: number;
  balance_error?: string;
  fetched_at: string;
}

export interface CredentialExtra {
  token_masked?: string;
}

export interface CatalogExtra {
  /** Shows the hint hook. */
  keys?: number;
}

export interface CredentialSaveExtra {
  token?: string;
}

export interface EditorExtra {
  /** A secret like an issuing token: blanked when the dialog closes. */
  token: string;
}
