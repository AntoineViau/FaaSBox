import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';
import { RouterLink } from '@angular/router';

import { DEMO_MODE_HINT } from '@/instance/instance.service';
import type { FaasboxFunction } from '@/models/faasbox-function.model';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

@Component({
  selector: 'app-sidebar',
  standalone: true,
  imports: [RouterLink, ZardButtonComponent, ZardIconComponent],
  template: `
    <div class="flex h-full flex-col">
      <!-- Always here, functions or not: a key with an open scope is written
           before the functions it will govern, and hiding the entry until the
           first function exists made the page unreachable from the editor. -->
      <div class="border-b border-border p-1">
        <a z-button zType="ghost" zSize="sm" class="w-full justify-start" routerLink="/keys">
          <z-icon zType="shield" class="mr-1.5 h-4 w-4" />
          API keys
        </a>
        <a z-button zType="ghost" zSize="sm" class="w-full justify-start" routerLink="/agents">
          <z-icon zType="sparkles" class="mr-1.5 h-4 w-4" />
          AI MCP
        </a>
      </div>
      <div class="flex items-center justify-between border-b border-border px-3 py-2">
        <span class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Functions</span>
        <div class="flex items-center">
          <!-- The list is only read on load: a function written from the API, or
               from another tab, shows up here on demand and not before. -->
          <button
            z-button
            zType="ghost"
            zSize="icon"
            class="h-7 w-7"
            title="Reload the list"
            [disabled]="loading()"
            (click)="refresh.emit()"
          >
            <z-icon zType="refresh-cw" class="h-4 w-4" [class.animate-spin]="loading()" />
          </button>
          <!-- The hint sits on the wrapper, not on the button: a browser sends
               no hover event to a disabled element, so a title placed on it
               would never show. -->
          <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
            <button
              z-button
              zType="ghost"
              zSize="icon"
              class="h-7 w-7"
              [disabled]="demoMode()"
              (click)="create.emit()"
            >
              <z-icon zType="plus" class="h-4 w-4" />
            </button>
          </span>
        </div>
      </div>
      <div class="flex-1 overflow-y-auto p-1">
        @if (functions().length === 0) {
          <p class="px-3 py-4 text-center text-xs text-muted-foreground">No functions yet.</p>
        }
        @for (fn of functions(); track fn.id) {
          <div
            class="group flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors"
            [class.bg-accent]="fn.id === selectedId()"
            [class.text-accent-foreground]="fn.id === selectedId()"
            [class.hover:bg-accent/50]="fn.id !== selectedId()"
            (click)="select.emit(fn.id)"
          >
            <z-icon zType="folder-code" class="h-4 w-4 shrink-0 text-muted-foreground" />
            <span class="flex-1 truncate">{{ fn.name }}</span>
            @if (cronFunctions().has(fn.id)) {
              <z-icon zType="calendar" class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            }
            <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
              <button
                z-button
                zType="ghost"
                zSize="icon"
                class="h-6 w-6 opacity-0 transition-opacity group-hover:opacity-100"
                [disabled]="demoMode()"
                (click)="$event.stopPropagation(); deleteItem.emit(fn.id)"
              >
                <z-icon zType="trash" class="h-3.5 w-3.5 text-destructive" />
              </button>
            </span>
          </div>
        }
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SidebarComponent {
  readonly functions = input.required<FaasboxFunction[]>();
  readonly selectedId = input.required<string | null>();
  /** Ids of the functions carrying at least one active trigger. */
  readonly cronFunctions = input<Set<string>>(new Set());
  /** Spins the reload icon and holds the button, so a click reads as an action. */
  readonly loading = input(false);
  /** A showcase lists the functions and closes what would write one. */
  readonly demoMode = input(false);

  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  readonly select = output<string>();
  readonly create = output<void>();
  readonly deleteItem = output<string>();
  readonly refresh = output<void>();
}
