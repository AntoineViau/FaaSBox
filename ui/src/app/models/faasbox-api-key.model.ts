export interface FaasboxApiKey {
  id: string;
  name: string;
  /** First 16 characters of the raw key. The key itself is never returned. */
  keyPrefix: string;
  /** Authorized function names. Empty or ["*"] means every function. */
  allowedFunctions: string[] | null;
  active: boolean;
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
