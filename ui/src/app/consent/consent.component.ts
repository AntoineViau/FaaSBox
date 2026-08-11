import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { ActivatedRoute } from '@angular/router';
import { firstValueFrom } from 'rxjs';

import { AuthService } from '@/auth/auth.service';
import { ConsentService, type ConsentRequest } from '@/consent/consent.service';
import { errorText } from '@/editor/error-text';
import { ThemeToggleComponent } from '@/theme/theme-toggle.component';
import { ZardAlertComponent } from '@shared/components/alert';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardCardComponent } from '@shared/components/card';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * The one step of the OAuth flow where a human is required.
 *
 * The page exists at `/consent` and not at `/oauth/authorize` because it cannot
 * live there: the server owns that path, and a top-level navigation carries no
 * Authorization header, so the server could not gate it on a session anyway. The
 * server validates the request, records it, and sends the browser here with an
 * opaque identifier — the SPA never carries an OAuth parameter it could alter.
 *
 * There is nothing to tick. A token grants everything, so the screen has to say
 * so in words, and those words come from the server: one source for what a token
 * is worth, rather than a list here that drifts from the one that is enforced.
 *
 * Deciding leaves the application. `window.location.href` is the point: the
 * redirection goes back to the agent that started the flow, which is not an
 * Angular route and usually not even this origin.
 */
@Component({
  selector: 'app-consent',
  standalone: true,
  imports: [
    ThemeToggleComponent,
    ZardAlertComponent,
    ZardButtonComponent,
    ZardCardComponent,
    ZardIconComponent,
  ],
  template: `
    <div class="mx-auto flex h-screen w-full max-w-app flex-col">
      <header class="flex items-center justify-between border-b border-border px-4 py-2">
        <h1 class="text-lg font-semibold">Authorize an agent</h1>
        <div class="flex items-center gap-2">
          <app-theme-toggle />
          <button z-button zType="outline" zSize="sm" (click)="logout()">
            <z-icon zType="log-out" class="mr-1.5 h-4 w-4" />
            Sign out
          </button>
        </div>
      </header>

      <div class="flex flex-1 items-center justify-center overflow-y-auto p-4">
        @if (loading()) {
          <p class="text-sm text-muted-foreground">Loading the request…</p>
        } @else if (failure()) {
          <z-card zTitle="This request cannot be shown" class="w-full max-w-lg">
            <z-alert zType="destructive" zTitle="Error" [zDescription]="failure()" />
            <p class="mt-3 text-sm text-muted-foreground">
              An authorization request is valid for a few minutes and can be answered once. Start the
              connection again from your agent.
            </p>
          </z-card>
        } @else if (request(); as consent) {
          <z-card
            [zTitle]="consent.client + ' wants to connect to FaaSBox'"
            zDescription="Authorizing gives it a token. There is nothing to choose: the token carries everything below."
            class="w-full max-w-lg"
          >
            <ul class="mb-4 space-y-1.5 text-sm">
              @for (grant of consent.grants; track grant) {
                <li class="flex gap-2">
                  <z-icon zType="check" class="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                  <span>{{ grant }}</span>
                </li>
              }
            </ul>

            <p class="mb-4 text-xs text-muted-foreground">
              You will be sent back to
              <code class="font-mono text-foreground">{{ consent.redirectUri }}</code
              >. Refuse if you did not start this from an agent on your own machine.
            </p>

            @if (decisionError()) {
              <z-alert
                zType="destructive"
                zTitle="Error"
                [zDescription]="decisionError()"
                class="mb-4 block"
              />
            }

            <div class="flex justify-end gap-2">
              <button z-button zType="outline" [zDisabled]="deciding()" (click)="decide(false)">
                Refuse
              </button>
              <button z-button [zLoading]="deciding()" [zDisabled]="deciding()" (click)="decide(true)">
                Authorize
              </button>
            </div>
          </z-card>
        }
      </div>
    </div>
  `,
  styles: `
    :host {
      display: block;
      height: 100vh;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ConsentComponent {
  private readonly route = inject(ActivatedRoute);
  private readonly consentService = inject(ConsentService);
  private readonly authService = inject(AuthService);

  protected readonly loading = signal(true);
  protected readonly deciding = signal(false);
  protected readonly request = signal<ConsentRequest | null>(null);
  /** Why the request cannot be shown at all — unknown, answered, or expired. */
  protected readonly failure = signal('');
  /** Why the last click failed, with the request still on screen. */
  protected readonly decisionError = signal('');

  private readonly requestId = this.route.snapshot.queryParamMap.get('request') ?? '';

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    if (!this.requestId) {
      this.failure.set('This page was opened without an authorization request.');
      this.loading.set(false);
      return;
    }
    try {
      this.request.set(await firstValueFrom(this.consentService.load(this.requestId)));
    } catch (error) {
      this.failure.set(errorText(error));
    } finally {
      this.loading.set(false);
    }
  }

  protected async decide(approve: boolean): Promise<void> {
    this.deciding.set(true);
    this.decisionError.set('');
    try {
      const decision = await firstValueFrom(this.consentService.decide(this.requestId, approve));
      window.location.href = decision.redirectUrl;
    } catch (error) {
      this.decisionError.set(errorText(error));
      this.deciding.set(false);
    }
  }

  protected logout(): void {
    this.authService.logout();
  }
}
