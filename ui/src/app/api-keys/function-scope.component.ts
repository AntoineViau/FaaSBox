import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  input,
  output,
  signal,
} from '@angular/core';

/** Wildcard scope: every function is allowed. */
export const ALL_FUNCTIONS = '*';

/** A function a scope can point at: recorded by id, shown by name. */
export interface ScopeFunction {
  id: string;
  name: string;
}

/**
 * Reports whether a stored scope authorizes every function. Mirrors the server
 * rule: an empty list carries no restriction, "*" is the wildcard.
 */
export function isUnrestrictedScope(scope: string[]): boolean {
  return scope.length === 0 || scope.includes(ALL_FUNCTIONS);
}

/**
 * Names a scope entry for display. An id matching no function is rendered as
 * itself rather than hidden: it is a dangling grant, and saying so is the only
 * way anyone will ever clean it up.
 */
export function describeScopeEntry(functions: readonly ScopeFunction[], id: string): string {
  return functions.find((fn) => fn.id === id)?.name ?? id;
}

/**
 * Function scope picker, shared by the creation form and the key list.
 *
 * It emits a list of function **ids**, or `null` when the selection is not
 * usable yet — "selected functions" mode with nothing checked. Never an empty
 * list: an empty scope means "no restriction" server-side, so persisting one by
 * accident would widen the key instead of narrowing it.
 *
 * Ids are what gets recorded and names are what gets shown, which is the whole
 * point: renaming a function leaves every key that grants it untouched.
 */
@Component({
  selector: 'app-function-scope',
  standalone: true,
  template: `
    <div class="space-y-2">
      <div class="flex items-center gap-4">
        <label class="flex cursor-pointer items-center gap-1.5">
          <input
            type="radio"
            class="h-3.5 w-3.5 accent-primary"
            [name]="groupName()"
            [checked]="mode() === 'all'"
            (change)="setMode('all')"
          />
          <span class="text-xs">All functions</span>
        </label>
        <label class="flex cursor-pointer items-center gap-1.5">
          <input
            type="radio"
            class="h-3.5 w-3.5 accent-primary"
            [name]="groupName()"
            [checked]="mode() === 'selected'"
            (change)="setMode('selected')"
          />
          <span class="text-xs">Selected functions</span>
        </label>
      </div>

      @if (mode() === 'selected') {
        @if (functions().length === 0) {
          <p class="text-xs text-muted-foreground">No function to select yet.</p>
        } @else {
          <div class="flex max-h-32 flex-wrap gap-x-4 gap-y-1 overflow-y-auto rounded-md border border-border p-2">
            @for (fn of functions(); track fn.id) {
              <label class="flex cursor-pointer items-center gap-1.5">
                <input
                  type="checkbox"
                  class="h-3.5 w-3.5 accent-primary"
                  [checked]="selected().has(fn.id)"
                  (change)="toggle(fn.id, $any($event.target).checked)"
                />
                <span class="font-mono text-xs">{{ fn.name }}</span>
              </label>
            }
          </div>
          @if (selected().size === 0) {
            <p class="text-xs text-destructive">Select at least one function.</p>
          }
        }
      }
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FunctionScopeComponent {
  /** Every function the scope can point at. */
  readonly functions = input.required<ScopeFunction[]>();
  /** Current scope, as stored: ids. Empty or containing "*" means every function. */
  readonly value = input<string[]>([]);
  /** Radio group name, unique per instance on a page holding several pickers. */
  readonly groupName = input('function-scope');

  readonly valueChange = output<string[] | null>();

  protected readonly mode = signal<'all' | 'selected'>('all');
  protected readonly selected = signal<ReadonlySet<string>>(new Set<string>());

  private readonly incoming = computed(() => this.value() ?? []);

  constructor() {
    effect(() => {
      const scope = this.incoming();
      const isAll = isUnrestrictedScope(scope);
      this.mode.set(isAll ? 'all' : 'selected');
      this.selected.set(new Set(isAll ? [] : scope));
    });
  }

  protected setMode(mode: 'all' | 'selected'): void {
    this.mode.set(mode);
    this.emit();
  }

  protected toggle(functionId: string, checked: boolean): void {
    const next = new Set(this.selected());
    if (checked) {
      next.add(functionId);
    } else {
      next.delete(functionId);
    }
    this.selected.set(next);
    this.emit();
  }

  private emit(): void {
    if (this.mode() === 'all') {
      // An explicit wildcard, never an empty list: it states the intent and
      // stays readable in the admin UI.
      this.valueChange.emit([ALL_FUNCTIONS]);
      return;
    }
    const ids = [...this.selected()];
    this.valueChange.emit(ids.length > 0 ? ids : null);
  }
}
