import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';
import { catchError, map, of } from 'rxjs';

import type {
  FaasboxOAuthGrant,
  FaasboxOAuthGrantListResponse,
} from '@/models/faasbox-oauth-grant.model';

const GRANTS_URL = '/api/collections/faasbox_oauth_grants/records';

/**
 * The document a client reads first. Asking for it is how this page learns
 * whether the instance can authorize an agent at all: it is served only when
 * FAASBOX_PUBLIC_URL named an issuer.
 *
 * The signal is the very document the agent would discover, rather than a flag
 * of our own that could say yes while the routes are down.
 */
const DISCOVERY_URL = '/.well-known/oauth-protected-resource';

/**
 * The fields the grants list reads, spelled out.
 *
 * This is the line that keeps the token hashes off the wire. They are `Hidden`
 * on the collection, but PocketBase only strips hidden fields for
 * non-superusers, and the editor is authenticated as one — so asking for the
 * whole record would ship them to the browser.
 */
const GRANT_FIELDS = 'id,created,refreshExpiresAt,expand.client.name,expand.client.clientId';

@Injectable({ providedIn: 'root' })
export class AgentsService {
  private readonly http = inject(HttpClient);

  /**
   * Whether this instance can hand an agent a token.
   *
   * **The status code is not the signal, the body is.** When the OAuth routes
   * are not mounted, nothing claims that path — so the SPA catch-all serves
   * `index.html` for it, with a `200`. Reading the answer as JSON and looking
   * for the one field the document must carry is what tells the two apart: HTML
   * fails to parse and lands in the refusal below.
   *
   * A refusal is not an error to report either way: the page simply shows the
   * snippets that carry a key.
   */
  oauthAvailable() {
    return this.http.get<{ resource?: string }>(DISCOVERY_URL).pipe(
      map((document) => !!document?.resource),
      catchError(() => of(false)),
    );
  }

  /**
   * The agents currently plugged in.
   *
   * Only the active ones: a revoked grant is not an authorization any more, and
   * the hourly purge collects it. `client` is expanded because the name the
   * agent registered lives on the other record.
   */
  listGrants() {
    return this.http.get<FaasboxOAuthGrantListResponse>(GRANTS_URL, {
      params: {
        filter: 'status="active"',
        expand: 'client',
        fields: GRANT_FIELDS,
        sort: '-created',
        perPage: '200',
      },
    });
  }

  /**
   * Revoking is a flag, not a deletion: the token hashes stay in place so a
   * later replay resolves to this grant and is refused by its state instead of
   * looking like an unknown credential. It takes effect on the next request the
   * agent makes — the grant is read on every call, and there is no session to
   * invalidate.
   */
  revoke(id: string) {
    return this.http.patch<FaasboxOAuthGrant>(`${GRANTS_URL}/${encodeURIComponent(id)}`, {
      status: 'revoked',
    });
  }
}
