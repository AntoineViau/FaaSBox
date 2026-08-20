import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  input,
  output,
  signal,
} from '@angular/core';

import {
  FunctionScopeComponent,
  type ScopeFunction,
} from '@/api-keys/function-scope.component';
import { DEMO_MODE_HINT } from '@/instance/instance.service';
import { ZardAlertComponent } from '@shared/components/alert';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

export interface ApiKeyCreateRequest {
  name: string;
  /** Function ids, or ["*"] for every function. */
  allowedFunctions: string[];
  /** RFC3339 date, or an empty string when the key never expires. */
  expiresAt: string;
  /** Whether the key may create, replace and delete functions. */
  canManage: boolean;
}

/**
 * Days the form proposes as an expiry once the management flag is ticked.
 *
 * A leaked management key lets someone write the code this server runs, where a
 * leaked invocation key only calls what is already there. The proposal is a
 * nudge, not a rule: the field stays editable, and clearing it still creates a
 * key that never expires.
 */
const MANAGE_KEY_EXPIRY_DAYS = 30;

@Component({
  selector: 'app-api-key-create',
  standalone: true,
  imports: [
    FunctionScopeComponent,
    ZardAlertComponent,
    ZardButtonComponent,
    ZardIconComponent,
    ZardInputDirective,
  ],
  template: `
    <!-- The reveal panel wears the warning accent rather than the default card
         background: creating a key also refreshes the list below it, and a
         panel that reads like the rest of the page loses the one value the
         user has a single chance to copy. Same accent as the editor banners. -->
    @if (createdKey(); as key) {
      <z-alert
        class="mb-4 border-yellow-500/30 bg-yellow-500/10 text-yellow-600 dark:text-yellow-500"
        zIcon="shield"
        zTitle="API key created"
        [zDescription]="revealTpl"
      />
      <ng-template #revealTpl>
        <p class="mb-2 text-yellow-600 dark:text-yellow-500">
          Copy it now: this value is shown once and will never be displayed again.
        </p>
        <div class="flex items-center gap-2">
          <code
            class="flex-1 break-all rounded-md border border-yellow-500/30 bg-background px-2 py-1 font-mono text-xs text-foreground"
            >{{ key }}</code
          >
          <button z-button zType="outline" zSize="sm" (click)="copy(key)">
            <z-icon zType="copy" class="mr-1.5 h-4 w-4" />
            {{ copied() ? 'Copied' : 'Copy' }}
          </button>
          <button z-button zType="ghost" zSize="sm" (click)="dismiss.emit()">Dismiss</button>
        </div>
        @if (copyFailed()) {
          <p class="mt-2">This browser refused the clipboard write — select the value above and copy it by hand.</p>
        }
      </ng-template>
    }

    <div class="mb-6 rounded-lg border border-border p-4">
      <h2 class="mb-3 text-sm font-semibold">Create a key</h2>

      <div class="space-y-3">
        <div>
          <label class="mb-1 block text-xs text-muted-foreground">Name</label>
          <input
            z-input
            type="text"
            class="h-8 max-w-sm text-sm"
            placeholder="my-application"
            [value]="name()"
            [disabled]="demoMode()"
            (input)="name.set($any($event.target).value)"
          />
        </div>

        <div>
          <label class="mb-1 block text-xs text-muted-foreground">Scope</label>
          <app-function-scope
            groupName="create-scope"
            [functions]="functions()"
            [value]="scopeSeed()"
            [demoMode]="demoMode()"
            (valueChange)="scope.set($event)"
          />
        </div>

        <div>
          <label class="flex w-fit cursor-pointer items-center gap-1.5">
            <input
              type="checkbox"
              class="h-4 w-4 accent-primary"
              [checked]="canManage()"
              [disabled]="demoMode()"
              (change)="onCanManageChange($any($event.target).checked)"
            />
            <span class="text-xs text-muted-foreground">Can manage functions</span>
          </label>
          <p class="mt-0.5 text-xs text-muted-foreground">
            Lets the key create, replace and delete functions — not just call them. That is arbitrary
            code execution on this server, so give it an expiration.
          </p>
        </div>

        <div>
          <label class="mb-1 block text-xs text-muted-foreground">Expiration (optional)</label>
          <input
            z-input
            type="date"
            class="h-8 w-44 text-sm"
            [value]="expiryDate()"
            [disabled]="demoMode()"
            (input)="expiryDate.set($any($event.target).value)"
          />
          <p class="mt-0.5 text-xs text-muted-foreground">
            The key stays valid through the end of that day (UTC). Leave empty for no expiration.
          </p>
        </div>

        <!-- The hint goes on the wrapper: a disabled button gets no hover
             event, so a title placed on it would never show. -->
        <span class="block w-fit" [title]="demoMode() ? DEMO_MODE_HINT : ''">
          <button z-button zType="default" zSize="sm" [disabled]="!canSubmit()" (click)="submit()">
            <z-icon zType="plus" class="mr-1.5 h-4 w-4" />
            Create key
          </button>
        </span>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ApiKeyCreateComponent {
  /** Functions the new key can be scoped to. */
  readonly functions = input.required<ScopeFunction[]>();
  /** Raw key returned by the last creation, or null when there is none to show. */
  readonly createdKey = input<string | null>(null);
  /** A showcase shows the form and closes every field and the button. */
  readonly demoMode = input(false);

  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  readonly create = output<ApiKeyCreateRequest>();
  readonly dismiss = output<void>();

  protected readonly name = signal('');
  protected readonly expiryDate = signal('');
  protected readonly canManage = signal(false);
  protected readonly copied = signal(false);
  protected readonly copyFailed = signal(false);

  /** Emitted scope; null while the picker holds an unusable selection. */
  protected readonly scope = signal<string[] | null>(['*']);
  /** Scope handed to the picker. Only a reset changes it, so typing never fights the form. */
  protected readonly scopeSeed = signal<string[]>(['*']);

  protected readonly canSubmit = computed(
    () => this.name().trim() !== '' && this.scope() !== null && !this.demoMode(),
  );

  constructor() {
    effect(() => {
      // A revealed key means the creation went through: clear the form so the
      // next key does not inherit the previous one's settings.
      if (this.createdKey()) {
        this.name.set('');
        this.expiryDate.set('');
        this.canManage.set(false);
        this.scope.set(['*']);
        this.scopeSeed.set(['*']);
        this.copied.set(false);
        this.copyFailed.set(false);
      }
    });
  }

  /**
   * Ticking the box proposes an expiry, and only when the field is still empty:
   * a date the user typed is theirs, and unticking never takes one away.
   */
  protected onCanManageChange(checked: boolean): void {
    this.canManage.set(checked);
    if (checked && this.expiryDate() === '') {
      const day = new Date();
      day.setDate(day.getDate() + MANAGE_KEY_EXPIRY_DAYS);
      this.expiryDate.set(day.toISOString().slice(0, 10));
    }
  }

  protected submit(): void {
    const allowedFunctions = this.scope();
    if (!this.canSubmit() || allowedFunctions === null) return;

    const day = this.expiryDate();
    this.create.emit({
      name: this.name().trim(),
      allowedFunctions,
      // <input type="date"> yields YYYY-MM-DD; the key expires at the end of
      // that day rather than at its first second.
      expiresAt: day ? `${day}T23:59:59Z` : '',
      canManage: this.canManage(),
    });
  }

  protected async copy(key: string): Promise<void> {
    // The clipboard API is unavailable outside a secure context, and may be
    // denied. Say so rather than losing the only copy of the key to a silent
    // rejection.
    try {
      await navigator.clipboard.writeText(key);
      this.copied.set(true);
      this.copyFailed.set(false);
    } catch {
      this.copyFailed.set(true);
    }
  }
}
