/**
 * One authorization an agent holds, as the page that revokes it reads it.
 *
 * The record carries far more than this — the hashes of the code, the access
 * token and the refresh token. They are not listed here and they are not asked
 * for: the service spells out the fields it wants, which is what keeps them off
 * the wire. Marking them `Hidden` is not enough on its own, since PocketBase
 * strips hidden fields for non-superusers and the editor is a superuser.
 */
export interface FaasboxOAuthGrant {
  id: string;
  /** When the authorization was opened, which is when the user clicked. */
  created: string;
  /** When the refresh token dies, and with it the agent's ability to renew. */
  refreshExpiresAt: string;
  expand?: {
    client?: {
      /** What the client registered as. A registration may carry none. */
      name?: string;
      clientId?: string;
    };
  };
}

export interface FaasboxOAuthGrantListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: FaasboxOAuthGrant[];
}

/**
 * What to call an agent on screen.
 *
 * Mirrors `clientDisplayName` server-side: a registration is only *recommended*
 * to carry a name, and the identifier is then the only honest thing to show.
 */
export function grantClientName(grant: FaasboxOAuthGrant): string {
  const client = grant.expand?.client;
  return client?.name?.trim() || client?.clientId || 'Unnamed client';
}
