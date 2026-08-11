import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

/** What the consent screen shows, as the server describes it. */
export interface ConsentRequest {
  /** The name the client registered, or its identifier when it registered none. */
  readonly client: string;
  readonly redirectUri: string;
  readonly scope: string;
  /** What authorizing hands over, in sentences. Written server-side. */
  readonly grants: readonly string[];
}

/** Where the browser goes once the decision is recorded. */
export interface ConsentDecision {
  readonly redirectUrl: string;
}

const BASE_URL = '/api/faasbox/oauth/requests';

/**
 * The two superuser calls behind the consent page.
 *
 * They are FaaSBox routes rather than the PocketBase collections API: the
 * authorization request is not a record to browse, and the code it mints must
 * never travel anywhere but the redirection the server builds.
 */
@Injectable({ providedIn: 'root' })
export class ConsentService {
  private readonly http = inject(HttpClient);

  load(id: string) {
    return this.http.get<ConsentRequest>(`${BASE_URL}/${encodeURIComponent(id)}`);
  }

  decide(id: string, approve: boolean) {
    return this.http.post<ConsentDecision>(`${BASE_URL}/${encodeURIComponent(id)}/decision`, {
      approve,
    });
  }
}
