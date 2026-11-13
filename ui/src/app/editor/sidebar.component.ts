import { ChangeDetectionStrategy, Component, HostListener, input, output, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { CircleQuestionMark } from 'lucide-angular';

import { DEMO_MODE_HINT, PAGES_OFF_HINT } from '@/instance/instance.service';
import type { FaasboxFunction } from '@/models/faasbox-function.model';
import type { FaasboxTrigger } from '@/models/faasbox-trigger.model';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * What a function's active triggers make of it, as the list shows it: run on a
 * schedule, run when the server comes up, published on a public route — any
 * combination of the three.
 */
export interface FunctionBadges {
  scheduled: boolean;
  startup: boolean;
  public: boolean;
}

/**
 * Folds the triggers into one entry per function id. Inactive triggers count
 * for nothing: a switched-off schedule runs nothing, a switched-off route
 * serves nothing. An empty kind reads as 'cron', as the server reads it.
 */
export function badgesOf(triggers: FaasboxTrigger[]): Map<string, FunctionBadges> {
  const badges = new Map<string, FunctionBadges>();
  for (const trigger of triggers) {
    if (!trigger.active) continue;
    const entry = badges.get(trigger.function) ?? { scheduled: false, startup: false, public: false };
    if (trigger.kind === 'route') {
      entry.public = true;
    } else if (trigger.kind === 'startup') {
      entry.startup = true;
    } else {
      entry.scheduled = true;
    }
    badges.set(trigger.function, entry);
  }
  return badges;
}

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
          <!-- Nothing is read again on its own: a function written from the API,
               by an agent or from another tab shows up here on demand and not
               before - and so does what they changed in the one that is open. -->
          <button
            z-button
            zType="ghost"
            zSize="icon"
            class="h-7 w-7"
            title="Reload"
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
            class="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors"
            [class.bg-accent]="fn.id === selectedId()"
            [class.text-accent-foreground]="fn.id === selectedId()"
            [class.hover:bg-accent/50]="fn.id !== selectedId()"
            (click)="select.emit(fn.id)"
          >
            <!-- min-w-0 lets the name truncate inside the flex row; the
                 badges sit on a line of their own under it, so the name keeps
                 the full width whatever the badges say. -->
            <div class="flex min-w-0 flex-1 flex-col">
              <span class="truncate">{{ fn.name }}</span>
              <!-- Words rather than an icon: a calendar said "scheduled" on a
                   function that was only published. "public" wears the warning
                   accent of the editor banners — anyone can run that one. When
                   the server serves no page, the route is armed and reaches no
                   one: the badge is struck through and a "?" says why. -->
              @if (badges().get(fn.id); as badge) {
                <!-- relative: the "?" tooltip spans the width of this line, which
                     is the width the narrow sidebar leaves. -->
                <div class="relative mt-0.5 flex flex-wrap items-center gap-1">
                  @if (badge.scheduled) {
                    <span
                      class="rounded-full border border-border bg-muted px-1.5 py-px text-[10px] leading-4 text-muted-foreground"
                      >scheduled</span
                    >
                  }
                  @if (badge.startup) {
                    <span
                      class="rounded-full border border-border bg-muted px-1.5 py-px text-[10px] leading-4 text-muted-foreground"
                      >startup</span
                    >
                  }
                  @if (badge.public) {
                    @if (pagesServed()) {
                      <span
                        class="rounded-full border border-yellow-500/30 bg-yellow-500/10 px-1.5 py-px text-[10px] leading-4 text-yellow-600 dark:text-yellow-500"
                        >public</span
                      >
                    } @else {
                      <span
                        class="rounded-full border border-border px-1.5 py-px text-[10px] leading-4 text-muted-foreground line-through"
                        >public</span
                      >
                      <!-- Shown on hover, on keyboard focus, and on a tap, which
                           toggles it: a touch screen has no hover. A click or a
                           tap anywhere else, or Escape, closes it again. -->
                      <button
                        type="button"
                        class="peer/hint inline-flex rounded-full text-muted-foreground hover:text-foreground"
                        [attr.aria-label]="PAGES_OFF_HINT"
                        [attr.aria-expanded]="hintFor() === fn.id"
                        (click)="$event.stopPropagation(); toggleHint(fn.id)"
                      >
                        <!-- Handed over as icon data: the registry is vendored
                             in shared/ and is not ours to extend. -->
                        <z-icon [zType]="QuestionIcon" class="h-3.5 w-3.5" />
                      </button>
                      <span
                        role="tooltip"
                        class="pointer-events-none absolute left-0 right-0 top-full z-20 mt-1 rounded-md border border-border bg-card px-2 py-1 text-[11px] leading-snug text-card-foreground shadow-md peer-hover/hint:block peer-focus-visible/hint:block"
                        [class.hidden]="hintFor() !== fn.id"
                        [class.block]="hintFor() === fn.id"
                        >{{ PAGES_OFF_HINT }}</span
                      >
                    }
                  }
                </div>
              }
            </div>
            <!-- Always shown, on every row: a delete revealed by hovering did
                 not exist on a touch screen, and could not be found with a
                 mouse either until the pointer happened to cross it. -->
            <span class="shrink-0" [title]="demoMode() ? DEMO_MODE_HINT : ''">
              <button
                z-button
                zType="ghost"
                zSize="icon"
                class="h-6 w-6"
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
  /** What the active triggers of each function make of it, keyed by function id. */
  readonly badges = input<Map<string, FunctionBadges>>(new Map());
  /** Whether the server serves public pages at all; without them a route reaches no one. */
  readonly pagesServed = input(true);
  /** Spins the reload icon and holds the button, so a click reads as an action. */
  readonly loading = input(false);
  /** A showcase lists the functions and closes what would write one. */
  readonly demoMode = input(false);

  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;
  protected readonly PAGES_OFF_HINT = PAGES_OFF_HINT;
  protected readonly QuestionIcon = CircleQuestionMark;

  /** The function whose "?" was tapped open, if any. Hover and focus need no state. */
  protected readonly hintFor = signal<string | null>(null);

  protected toggleHint(id: string): void {
    this.hintFor.update((open) => (open === id ? null : id));
  }

  // The "?" stops its own click, so whatever reaches the document came from
  // somewhere else: a tooltip opened by a tap does not stay pinned.
  @HostListener('document:click')
  @HostListener('document:keydown.escape')
  protected closeHint(): void {
    this.hintFor.set(null);
  }

  readonly select = output<string>();
  readonly create = output<void>();
  readonly deleteItem = output<string>();
  readonly refresh = output<void>();
}
