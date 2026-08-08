import { ChangeDetectionStrategy, Component, inject, type OnInit, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';

import { ApiKeyCreateComponent, type ApiKeyCreateRequest } from '@/api-keys/api-key-create.component';
import { ApiKeysService } from '@/api-keys/api-keys.service';
import {
  describeScopeEntry,
  FunctionScopeComponent,
  isUnrestrictedScope,
  type ScopeFunction,
} from '@/api-keys/function-scope.component';
import { FunctionsService } from '@/editor/functions.service';
import type { FaasboxApiKey } from '@/models/faasbox-api-key.model';
import { ThemeToggleComponent } from '@/theme/theme-toggle.component';
import { ZardAlertComponent } from '@shared/components/alert';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * A key whose scope is always a list.
 *
 * The picker binds to that list directly: a `?? []` in the template would hand
 * it a fresh array on every change detection run and reset the selection under
 * the user's fingers.
 */
type ApiKeyRow = FaasboxApiKey & { allowedFunctions: string[] };

/**
 * PocketBase returns an unset JSON field as null, and a hand-edited one may not
 * even be a list. Both collapse to an empty scope, which the server already
 * reads as "no restriction".
 */
function asRow(key: FaasboxApiKey): ApiKeyRow {
  return Array.isArray(key.allowedFunctions) ? (key as ApiKeyRow) : { ...key, allowedFunctions: [] };
}

@Component({
  selector: 'app-api-keys',
  standalone: true,
  imports: [
    RouterLink,
    ApiKeyCreateComponent,
    FunctionScopeComponent,
    ThemeToggleComponent,
    ZardAlertComponent,
    ZardButtonComponent,
    ZardIconComponent,
  ],
  template: `
    <div class="mx-auto flex h-screen w-full max-w-app flex-col">
      <header class="flex items-center justify-between border-b border-border px-4 py-2">
        <div class="flex items-center gap-3">
          <a z-button zType="ghost" zSize="sm" routerLink="/editor">
            <z-icon zType="arrow-left" class="mr-1.5 h-4 w-4" />
            Functions
          </a>
          <h1 class="text-lg font-semibold">API keys</h1>
        </div>
        <app-theme-toggle />
      </header>

      <div class="flex-1 overflow-y-auto p-4">
        @if (errorMessage()) {
          <z-alert class="mb-4" zType="destructive" zTitle="Error" [zDescription]="errorMessage()" />
        }

        <app-api-key-create
          [functions]="functions()"
          [createdKey]="createdKey()"
          (create)="onCreate($event)"
          (dismiss)="createdKey.set(null)"
        />

        @if (isLoading()) {
          <p class="text-sm text-muted-foreground">Loading keys...</p>
        } @else if (keys().length === 0) {
          <p class="text-sm text-muted-foreground">No API key yet.</p>
        } @else {
          @for (key of keys(); track key.id) {
            <div class="mb-3 rounded-lg border border-border p-3">
              <div class="flex items-start gap-3">
                <div class="flex-1 space-y-2">
                  <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                    <span class="text-sm font-semibold">{{ key.name }}</span>
                    <code class="font-mono text-xs text-muted-foreground">{{ key.keyPrefix }}...</code>
                    @if (key.canManage) {
                      <span class="rounded border border-border px-1.5 py-0.5 text-xs text-muted-foreground">
                        Can manage functions
                      </span>
                    }
                  </div>
                  <p class="text-xs text-muted-foreground">
                    Created {{ day(key.created) }} —
                    {{ key.expiresAt ? 'expires ' + day(key.expiresAt) : 'never expires' }}
                  </p>

                  <details>
                    <summary class="cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">
                      Scope: {{ describeScope(key.allowedFunctions) }}
                    </summary>
                    <div class="mt-2">
                      <app-function-scope
                        [groupName]="'scope-' + key.id"
                        [functions]="functions()"
                        [value]="key.allowedFunctions"
                        (valueChange)="onScopeChange(key, $event)"
                      />
                    </div>
                  </details>
                </div>

                <div class="flex flex-col items-center gap-2">
                  <label class="flex cursor-pointer items-center gap-1.5">
                    <input
                      type="checkbox"
                      class="h-4 w-4 accent-primary"
                      [checked]="key.active"
                      (change)="onToggleActive(key, $any($event.target).checked)"
                    />
                    <span class="text-xs text-muted-foreground">Active</span>
                  </label>
                  <button z-button zType="ghost" zSize="icon" class="h-7 w-7" (click)="onDelete(key)">
                    <z-icon zType="trash" class="h-3.5 w-3.5 text-destructive" />
                  </button>
                </div>
              </div>
            </div>
          }
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
export class ApiKeysComponent implements OnInit {
  private readonly apiKeysService = inject(ApiKeysService);
  private readonly functionsService = inject(FunctionsService);

  protected readonly keys = signal<ApiKeyRow[]>([]);
  /** Every function the scopes can point at: recorded by id, shown by name. */
  protected readonly functions = signal<ScopeFunction[]>([]);
  protected readonly createdKey = signal<string | null>(null);
  protected readonly isLoading = signal(false);
  protected readonly errorMessage = signal('');

  ngOnInit(): void {
    this.loadKeys();
    this.loadFunctions();
  }

  private async loadKeys(): Promise<void> {
    this.isLoading.set(true);
    try {
      const res = await firstValueFrom(this.apiKeysService.list());
      this.keys.set(res.items.map((key) => asRow(key)));
    } catch (e) {
      this.report(e, 'Failed to load API keys');
    } finally {
      this.isLoading.set(false);
    }
  }

  private async loadFunctions(): Promise<void> {
    // The editor runs as superuser, so the scope picker always sees every
    // function, whatever scope the keys themselves declare.
    const res = await firstValueFrom(this.functionsService.list());
    this.functions.set(res.items.map((fn) => ({ id: fn.id, name: fn.name })));
  }

  protected async onCreate(request: ApiKeyCreateRequest): Promise<void> {
    this.errorMessage.set('');
    try {
      const created = await firstValueFrom(
        this.apiKeysService.create(
          request.name,
          request.allowedFunctions,
          request.expiresAt,
          request.canManage,
        ),
      );
      // The raw value only lives in this signal, until the user leaves the page
      // or dismisses the panel.
      this.createdKey.set(created.key);
      await this.loadKeys();
    } catch (e) {
      this.report(e, 'Failed to create the API key');
    }
  }

  protected async onToggleActive(key: ApiKeyRow, active: boolean): Promise<void> {
    await this.patch(key, { active });
  }

  protected async onScopeChange(key: ApiKeyRow, scope: string[] | null): Promise<void> {
    // null means the picker holds an empty selection, which would read as "no
    // restriction" server-side. Nothing to persist until it is usable again.
    if (scope === null) return;
    await this.patch(key, { allowedFunctions: scope });
  }

  protected async onDelete(key: ApiKeyRow): Promise<void> {
    if (!confirm(`Delete API key "${key.name}"? Applications using it will stop working.`)) return;
    this.errorMessage.set('');
    try {
      await firstValueFrom(this.apiKeysService.delete(key.id));
      this.keys.update((list) => list.filter((k) => k.id !== key.id));
    } catch (e) {
      this.report(e, 'Failed to delete the API key');
    }
  }

  private async patch(key: ApiKeyRow, data: Partial<FaasboxApiKey>): Promise<void> {
    this.errorMessage.set('');
    try {
      const updated = await firstValueFrom(this.apiKeysService.update(key.id, data));
      this.keys.update((list) => list.map((k) => (k.id === key.id ? asRow(updated) : k)));
    } catch (e) {
      this.report(e, 'Failed to update the API key');
    }
  }

  /** The stored scope is a list of ids; the summary line reads in names. */
  protected describeScope(allowed: string[]): string {
    if (isUnrestrictedScope(allowed)) return 'all functions';
    return allowed.map((id) => describeScopeEntry(this.functions(), id)).join(', ');
  }

  /** PocketBase dates read "YYYY-MM-DD HH:MM:SS.sssZ"; the day is what matters here. */
  protected day(value: string): string {
    return value ? value.slice(0, 10) : '';
  }

  private report(error: unknown, fallback: string): void {
    const message = error instanceof Error ? error.message : '';
    this.errorMessage.set(message ? `${fallback}: ${message}` : fallback);
  }
}
