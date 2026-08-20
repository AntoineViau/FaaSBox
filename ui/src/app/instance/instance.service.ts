import { HttpClient } from '@angular/common/http';
import { inject, Injectable, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';

const INSTANCE_URL = '/api/faasbox/instance';

/**
 * The one wording of the tooltip every affordance the demo mode closes carries.
 *
 * It lives here rather than in a component so a dozen buttons cannot drift into
 * a dozen phrasings.
 */
export const DEMO_MODE_HINT = 'Disabled — this instance is a read-only demo';

interface InstanceResponse {
  demoMode?: boolean;
  email?: string;
  password?: string;
}

/**
 * What mode this instance runs in, read once before the first render.
 *
 * A showcase shows everything and runs nothing: the server refuses every write
 * method, and these signals are what closes the affordances that would ask for
 * one. The two credentials are only published by a demo instance, and they fill
 * the sign-in form so a visitor can open the ordinary session the editor needs.
 */
@Injectable({ providedIn: 'root' })
export class InstanceService {
  private readonly http = inject(HttpClient);

  private readonly demo = signal(false);
  private readonly email = signal('');
  private readonly password = signal('');

  readonly demoMode = this.demo.asReadonly();
  readonly demoEmail = this.email.asReadonly();
  readonly demoPassword = this.password.asReadonly();

  /**
   * Called by the application initializer, so nothing renders before the answer
   * is in: a button that appeared usable and then went grey would be worse than
   * one that was grey from the start.
   *
   * **A failure leaves the instance normal, on purpose.** The guarantee does not
   * live here — the server refuses whatever this says. Closing the editor on a
   * network hiccup would cost a working instance its editor and add nothing to
   * the safety.
   */
  async load(): Promise<void> {
    try {
      const info = await firstValueFrom(this.http.get<InstanceResponse>(INSTANCE_URL));
      this.demo.set(info?.demoMode === true);
      this.email.set(info?.email ?? '');
      this.password.set(info?.password ?? '');
    } catch (e) {
      console.warn('Could not read the instance mode; treating it as a normal instance.', e);
    }
  }
}
