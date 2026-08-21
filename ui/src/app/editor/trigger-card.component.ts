import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';

import { CronHelpComponent } from '@/editor/cron-help.component';
import { DEFAULT_SCHEDULE, describeSchedule } from '@/editor/cron-presets';
import { DEMO_MODE_HINT } from '@/instance/instance.service';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

/**
 * A trigger as the panel holds it on screen.
 *
 * `id` is empty until the row has been written, which is what tells a creation
 * from an update at save time; `key` identifies the row from the moment it is
 * added, so a never-saved trigger still has something stable to track.
 *
 * The payload stays the raw text that was typed: parsing it at save time is
 * what allows an invalid document to be reported instead of dropped.
 */
export interface TriggerRow {
  key: number;
  id: string;
  name: string;
  schedule: string;
  payload: string;
  active: boolean;
  maxQueue: number;
  /**
   * Which deadline fires this row. Held as the named discriminant the database
   * stores rather than a boolean: the log's own trigger column is already a
   * list of values, and a boolean would close the door on a fourth kind. The
   * checkbox on screen is a rendering of it.
   */
  kind: 'cron' | 'startup';
  /** On a startup row, how long after boot it fires. 0 to 1439. */
  startupDelayMinutes: number;
}

/**
 * 23:59, all the hours:minutes entry below can express — the bound is the shape
 * of the input. The server refuses anything past it.
 */
export const MAX_STARTUP_DELAY_MINUTES = 1439;

/** Presentational: it owns no state and writes nothing. */
@Component({
  selector: 'app-trigger-card',
  standalone: true,
  imports: [CronHelpComponent, ZardButtonComponent, ZardIconComponent, ZardInputDirective],
  template: `
    <div class="mb-3 rounded-lg border border-border p-3">
      <div class="flex items-start gap-3">
        <div class="flex-1 space-y-2">
          <!-- Name -->
          <div>
            <label class="mb-1 block text-xs text-muted-foreground">Name</label>
            <input
              z-input
              type="text"
              class="h-8 text-sm"
              [value]="row().name"
              [disabled]="demoMode()"
              (input)="rowChange.emit({ name: $any($event.target).value })"
            />
          </div>

          <!-- Which deadline fires this trigger -->
          <label class="flex w-fit cursor-pointer items-center gap-1.5">
            <input
              type="checkbox"
              [checked]="isStartup()"
              [disabled]="demoMode()"
              (change)="onStartupToggle($any($event.target).checked)"
              class="h-4 w-4 accent-primary"
            />
            <span class="text-xs text-muted-foreground">Startup trigger</span>
          </label>

          @if (isStartup()) {
            <!-- Delay -->
            <div>
              <label class="mb-1 block text-xs text-muted-foreground">Run this long after startup</label>
              <div class="flex items-center gap-1.5">
                <input
                  z-input
                  type="number"
                  min="0"
                  max="23"
                  step="1"
                  class="h-8 w-20 text-sm"
                  value="{{ delayHours() }}"
                  [disabled]="demoMode()"
                  (change)="onDelay('hours', $any($event.target))"
                />
                <span class="text-xs text-muted-foreground">h</span>
                <input
                  z-input
                  type="number"
                  min="0"
                  max="59"
                  step="1"
                  class="h-8 w-20 text-sm"
                  value="{{ delayMinutes() }}"
                  [disabled]="demoMode()"
                  (change)="onDelay('minutes', $any($event.target))"
                />
                <span class="text-xs text-muted-foreground">min</span>
              </div>
              <p class="mt-0.5 text-xs text-muted-foreground">
                Fires once, that long after the server comes up, and again at every
                restart. Changing it here does not fire it, and unticking it does not
                stop one already counting down: a restart settles both. 23h59 at most.
              </p>
            </div>
          } @else {
            <!-- Schedule -->
            <div>
              <label class="mb-1 block text-xs text-muted-foreground">Schedule (cron expression)</label>
              <input
                z-input
                type="text"
                class="h-8 font-mono text-sm"
                placeholder="*/5 * * * *"
                [value]="row().schedule"
                [disabled]="demoMode()"
                (input)="rowChange.emit({ schedule: $any($event.target).value })"
              />
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ describe(row().schedule) }}
              </p>
              <div class="mt-1.5">
                <app-cron-help (pick)="onPick($event)" />
              </div>
            </div>
          }

          <!-- Payload -->
          <div>
            <label class="mb-1 block text-xs text-muted-foreground">Payload (JSON)</label>
            <textarea
              z-input
              rows="2"
              class="font-mono text-sm"
              placeholder="{}"
              [value]="row().payload"
              [disabled]="demoMode()"
              (input)="rowChange.emit({ payload: $any($event.target).value })"
            ></textarea>
          </div>

          <!-- Advanced (collapsed by default) -->
          <details>
            <summary
              class="cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground"
            >
              Advanced
            </summary>
            <div class="mt-2">
              <label class="mb-1 block text-xs text-muted-foreground">Max queue</label>
              <input
                z-input
                type="number"
                min="0"
                step="1"
                class="h-8 w-28 text-sm"
                value="{{ row().maxQueue }}"
                [disabled]="demoMode()"
                (change)="onMaxQueue($any($event.target))"
              />
              <p class="mt-0.5 text-xs text-muted-foreground">
                Maximum simultaneous executions (waiting + running) for this trigger. Extra
                triggers are skipped with a warning in the server log. 0 means no limit.
              </p>
            </div>
          </details>
        </div>

        <!-- Right actions -->
        <div class="flex flex-col items-center gap-2 pt-5">
          <label class="flex cursor-pointer items-center gap-1.5">
            <input
              type="checkbox"
              [checked]="row().active"
              [disabled]="demoMode()"
              (change)="rowChange.emit({ active: $any($event.target).checked })"
              class="h-4 w-4 accent-primary"
            />
            <span class="text-xs text-muted-foreground">Active</span>
          </label>
          <!-- The hint goes on the wrapper: a disabled button gets no hover
               event, so a title placed on it would never show. -->
          <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
            <button
              z-button
              zType="ghost"
              zSize="icon"
              class="h-7 w-7"
              [disabled]="demoMode()"
              (click)="removeRow.emit()"
            >
              <z-icon zType="trash" class="h-3.5 w-3.5 text-destructive" />
            </button>
          </span>
        </div>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TriggerCardComponent {
  readonly row = input.required<TriggerRow>();
  /** A showcase shows a trigger and closes every field and button that writes it. */
  readonly demoMode = input(false);

  /** The fields the user just touched; the panel owns the list. */
  readonly rowChange = output<Partial<TriggerRow>>();
  readonly removeRow = output<void>();

  protected readonly describe = describeSchedule;
  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  /** The checkbox is a rendering of the discriminant, nothing more. */
  protected readonly isStartup = computed(() => this.row().kind === 'startup');
  protected readonly delayHours = computed(() =>
    Math.floor(this.row().startupDelayMinutes / 60),
  );
  protected readonly delayMinutes = computed(() => this.row().startupDelayMinutes % 60);

  /**
   * Emptying the schedule belongs to the toggle, not to the save. A row whose
   * expression was typed before the switch would otherwise travel as a startup
   * trigger carrying one, which the server refuses — and the user would read a
   * 400 about a field no longer on screen.
   */
  protected onStartupToggle(checked: boolean): void {
    if (this.demoMode()) return;
    this.rowChange.emit(
      checked
        ? { kind: 'startup', schedule: '' }
        : { kind: 'cron', schedule: DEFAULT_SCHEDULE },
    );
  }

  /**
   * Two fields rather than an `<input type="time">`: that widget draws a clock
   * and follows the browser locale, so it asks "at what time" when the question
   * is "how long after". The pair converts to the minutes the row holds, which
   * is what the column stores and what the bound is expressed in.
   */
  protected onDelay(field: 'hours' | 'minutes', input: HTMLInputElement): void {
    const parsed = Number.parseInt(input.value, 10);
    const value = Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
    const hours = field === 'hours' ? value : this.delayHours();
    const minutes = field === 'minutes' ? value : this.delayMinutes();
    const total = Math.min(hours * 60 + minutes, MAX_STARTUP_DELAY_MINUTES);

    // Re-sync the field when the typed text was normalized: rebinding alone
    // would not repaint it if the stored value did not change (0 -> "-5" -> 0).
    const shown = field === 'hours' ? Math.floor(total / 60) : total % 60;
    if (input.value !== String(shown)) {
      input.value = String(shown);
    }
    this.rowChange.emit({ startupDelayMinutes: total });
  }

  /** The preset list is a shortcut into the schedule field, so it closes with it. */
  protected onPick(schedule: string): void {
    if (this.demoMode()) return;
    this.rowChange.emit({ schedule });
  }

  protected onMaxQueue(input: HTMLInputElement): void {
    // The DOM yields a string; maxQueue is a PocketBase NumberField. Empty,
    // invalid and negative inputs all collapse to 0, which means "no limit"
    // server-side.
    const parsed = Number.parseInt(input.value, 10);
    const value = Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
    // Re-sync the field when the typed text was normalized: rebinding alone
    // would not repaint it if the stored value did not change (0 -> "-5" -> 0).
    if (input.value !== String(value)) {
      input.value = String(value);
    }
    this.rowChange.emit({ maxQueue: value });
  }
}
