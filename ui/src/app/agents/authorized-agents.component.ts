import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';

import { DEMO_MODE_HINT } from '@/instance/instance.service';
import { type FaasboxOAuthGrant, grantClientName } from '@/models/faasbox-oauth-grant.model';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * The agents that authorized themselves against this instance, and the button
 * that ends one.
 *
 * The counterpart of what the API keys page already does: this is the page where
 * one decides to cut an agent off, so it has to show what is currently plugged
 * in. Presentational — the list arrives as an input, the click leaves as an
 * output.
 */
@Component({
  selector: 'app-authorized-agents',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent],
  template: `
    <h2 class="mb-2 text-sm font-semibold">Authorized agents</h2>

    @if (isLoading()) {
      <p class="text-sm text-muted-foreground">Loading authorized agents...</p>
    } @else if (grants().length === 0) {
      <p class="text-sm text-muted-foreground">
        No agent has been authorized yet. One appears here the moment you approve it.
      </p>
    } @else {
      @for (grant of grants(); track grant.id) {
        <div class="mb-3 flex items-start gap-3 rounded-lg border border-border p-3">
          <div class="flex-1 space-y-1">
            <span class="text-sm font-semibold">{{ clientName(grant) }}</span>
            <p class="text-xs text-muted-foreground">
              Authorized {{ day(grant.created) }} —
              {{ grant.refreshExpiresAt ? 'expires ' + day(grant.refreshExpiresAt) : 'no expiry' }}
            </p>
          </div>
          <!-- The hint goes on the wrapper: a disabled button gets no hover
               event, so a title placed on it would never show. -->
          <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
            <button
              z-button
              zType="outline"
              zSize="sm"
              [zDisabled]="demoMode()"
              (click)="revoke.emit(grant)"
            >
              <z-icon zType="trash" class="mr-1.5 h-3.5 w-3.5 text-destructive" />
              Revoke
            </button>
          </span>
        </div>
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AuthorizedAgentsComponent {
  readonly grants = input.required<readonly FaasboxOAuthGrant[]>();
  readonly isLoading = input(false);
  /** A showcase lists the authorized agents and closes the button that cuts one off. */
  readonly demoMode = input(false);
  readonly revoke = output<FaasboxOAuthGrant>();

  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  protected clientName(grant: FaasboxOAuthGrant): string {
    return grantClientName(grant);
  }

  /** PocketBase dates read "YYYY-MM-DD HH:MM:SS.sssZ"; the day is what matters. */
  protected day(value: string): string {
    return value ? value.slice(0, 10) : '';
  }
}
