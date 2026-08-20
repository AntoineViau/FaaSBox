import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';

import { CronHelpComponent } from '@/editor/cron-help.component';
import { describeSchedule } from '@/editor/cron-presets';
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
export interface CronRow {
  key: number;
  id: string;
  name: string;
  schedule: string;
  payload: string;
  active: boolean;
  maxQueue: number;
}

/** Presentational: it owns no state and writes nothing. */
@Component({
  selector: 'app-cron-trigger-card',
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
export class CronTriggerCardComponent {
  readonly row = input.required<CronRow>();
  /** A showcase shows a trigger and closes every field and button that writes it. */
  readonly demoMode = input(false);

  /** The fields the user just touched; the panel owns the list. */
  readonly rowChange = output<Partial<CronRow>>();
  readonly removeRow = output<void>();

  protected readonly describe = describeSchedule;
  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

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
