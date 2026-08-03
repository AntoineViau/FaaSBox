import { ChangeDetectionStrategy, Component, output } from '@angular/core';

import { CRON_PRESET_LIST } from '@/editor/cron-presets';
import { ZardButtonComponent } from '@shared/components/button';

/**
 * What the five columns mean, plus the ready-made expressions as buttons.
 *
 * Collapsed by default, like the Advanced fold next to it: the card has to stay
 * readable at a glance for whoever already knows the syntax.
 */
@Component({
  selector: 'app-cron-help',
  standalone: true,
  imports: [ZardButtonComponent],
  template: `
    <details>
      <summary class="cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">
        Cron syntax
      </summary>

      <div class="mt-2 space-y-2">
        <pre
          class="overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px] leading-snug text-muted-foreground"
        >┌───────────── minute (0 - 59)
│ ┌───────────── hour (0 - 23)
│ │ ┌───────────── day of the month (1 - 31)
│ │ │ ┌───────────── month (1 - 12)
│ │ │ │ ┌───────────── day of the week (0 - 6, Sunday to Saturday)
│ │ │ │ │
* * * * *</pre
        >

        <p class="text-xs text-muted-foreground">Click an example to use it:</p>
        <div class="flex flex-wrap gap-1">
          @for (preset of presets; track preset.expression) {
            <button z-button zType="outline" zSize="xs" (click)="pick.emit(preset.expression)">
              <span class="font-mono">{{ preset.expression }}</span>
              <span class="ml-1.5 text-muted-foreground">{{ preset.label }}</span>
            </button>
          }
        </div>
      </div>
    </details>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CronHelpComponent {
  /** The expression the user clicked, to be written into the schedule field. */
  readonly pick = output<string>();

  protected readonly presets = CRON_PRESET_LIST;
}
