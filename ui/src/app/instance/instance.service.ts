import { HttpClient } from '@angular/common/http';
import { inject, Injectable, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';

const INSTANCE_URL = '/api/faasbox/instance';
const VERSION_URL = '/api/faasbox/version';

/**
 * The one wording of the tooltip every affordance the demo mode closes carries.
 *
 * It lives here rather than in a component so a dozen buttons cannot drift into
 * a dozen phrasings.
 */
export const DEMO_MODE_HINT = 'Disabled — this instance is a read-only demo';

interface InstanceResponse {
  demoMode?: boolean;
  name?: string;
  pagesUrl?: string;
  email?: string;
  password?: string;
}

interface VersionResponse {
  version?: string;
}

/**
 * What this instance is, read once before the first render.
 *
 * A showcase shows everything and runs nothing: the server refuses every write
 * method, and these signals are what closes the affordances that would ask for
 * one. The two credentials are only published by a demo instance, and they fill
 * the sign-in form so a visitor can open the ordinary session the editor needs.
 *
 * The name is the other facet, and it is independent of the mode: it labels the
 * sign-in card and the editor header, and names the MCP server in the
 * integration snippets, so whoever runs two boxes can tell one tab — and one
 * agent connection — from the other. An unnamed instance reads as the empty
 * string, which is every instance that does not set `FAASBOX_NAME`.
 *
 * **Reading it once is enough because the answer cannot be stale.** The response
 * forbids being stored, so this call always reaches the server: the freshness is
 * the response's own doing, and nothing here has to ask for it.
 *
 * Where the public pages are served is a third facet: the origin a function
 * opened by a route is published on, or the empty string when the server
 * serves none (no `FAASBOX_PAGES_URL`, or a showcase). The Triggers tab builds
 * the public URL from it, and turns the route option off without it.
 *
 * The build version is a fourth facet, read apart: it is served to a superuser
 * only, so it cannot be in the answer read before sign-in.
 */
@Injectable({ providedIn: 'root' })
export class InstanceService {
  private readonly http = inject(HttpClient);

  private readonly demo = signal(false);
  private readonly instanceName = signal('');
  private readonly pagesOrigin = signal('');
  private readonly email = signal('');
  private readonly password = signal('');
  private readonly buildVersion = signal('');

  readonly demoMode = this.demo.asReadonly();
  readonly name = this.instanceName.asReadonly();
  /** The origin of the public pages, '' when the server serves none. */
  readonly pagesUrl = this.pagesOrigin.asReadonly();
  readonly demoEmail = this.email.asReadonly();
  readonly demoPassword = this.password.asReadonly();
  /** The build that answers — a release tag such as 0.7.2, or dev. Empty until read. */
  readonly version = this.buildVersion.asReadonly();

  /**
   * Called by the application initializer, so nothing renders before the answer
   * is in: a button that appeared usable and then went grey would be worse than
   * one that was grey from the start.
   *
   * **A failure leaves the instance normal, on purpose.** The guarantee does not
   * live here — the server refuses whatever this says. Closing the editor on a
   * network hiccup would cost a working instance its editor and add nothing to
   * the safety. The name falls back to none for the same reason: an unnamed
   * header is the state every instance was in before the variable existed, and
   * a missing pages origin only greys an option out: whether /f/ answers is the
   * server's decision, not this one's.
   */
  async load(): Promise<void> {
    try {
      const info = await firstValueFrom(this.http.get<InstanceResponse>(INSTANCE_URL));
      this.demo.set(info?.demoMode === true);
      this.instanceName.set(info?.name ?? '');
      this.pagesOrigin.set(info?.pagesUrl ?? '');
      this.email.set(info?.email ?? '');
      this.password.set(info?.password ?? '');
    } catch (e) {
      console.warn('Could not read the instance mode; treating it as a normal instance.', e);
    }
  }

  /**
   * Called by the editor once signed in, since the route wants a superuser
   * session. A failure leaves the version empty, and an empty version is simply
   * not shown: nothing depends on it.
   */
  async loadVersion(): Promise<void> {
    try {
      const info = await firstValueFrom(this.http.get<VersionResponse>(VERSION_URL));
      this.buildVersion.set(info?.version ?? '');
    } catch (e) {
      console.warn('Could not read the version of this instance.', e);
    }
  }
}
