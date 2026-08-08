export interface FaasboxApiKey {
  id: string;
  name: string;
  /** First 16 characters of the raw key. The key itself is never returned. */
  keyPrefix: string;
  /**
   * Authorized function ids. Empty or ["*"] means every function.
   *
   * Ids and not names: a scope written in names stopped designating anything
   * the day a function was renamed.
   */
  allowedFunctions: string[] | null;
  active: boolean;
  /**
   * Whether the key may create, replace and delete functions.
   *
   * A second dimension, not a wider scope: reaching a function and rewriting it
   * are different rights. A key without it can only invoke what already exists.
   */
  canManage: boolean;
  /** Empty string means the key never expires. */
  expiresAt: string;
  created: string;
  updated: string;
}

export interface FaasboxApiKeyListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: FaasboxApiKey[];
}

/** Response of POST /api/faasbox/keys — the only call that returns the raw key. */
export interface FaasboxApiKeyCreated {
  key: string;
  name: string;
  note: string;
}
