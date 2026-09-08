import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';

/**
 * The zone the browser is in, read once at load.
 *
 * It seeds a new trigger: someone writing `0 3 * * *` means three in the morning
 * where they are, and UTC only by coincidence. It is also half of the fallback
 * list below, for a browser that cannot enumerate the catalogue.
 */
export const BROWSER_TIMEZONE = browserTimezone();

function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/**
 * The IANA catalogue the browser knows, or a two-entry fallback.
 *
 * `Intl.supportedValuesOf` needs Chrome 99+, Firefox 93+ or Safari 15.4+. Older
 * browsers get their own zone and UTC, which is enough to configure a trigger —
 * losing the picker must not take the field off the screen.
 */
function catalogue(): string[] {
  try {
    if (typeof Intl.supportedValuesOf === 'function') {
      const zones = Intl.supportedValuesOf('timeZone');
      if (zones.length) return zones;
    }
  } catch {
    /* falls through to the two-entry list */
  }
  return BROWSER_TIMEZONE === 'UTC' ? ['UTC'] : [BROWSER_TIMEZONE, 'UTC'];
}

const ZONES = catalogue();

/**
 * The zone a cron expression is read in.
 *
 * Presentational: it owns no state, emits no request, and holds no catalogue of
 * its own beyond the one the browser hands it. A component of its own rather
 * than a field added to the trigger card, on the model of `cron-help`: the card
 * is already long, and the list of zones is a concern in itself.
 */
@Component({
  selector: 'app-timezone-select',
  standalone: true,
  template: `
    <select
      class="flex h-8 w-full rounded-md border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50"
      [disabled]="disabled()"
      (change)="valueChange.emit($any($event.target).value)"
    >
      @for (zone of zones(); track zone) {
        <option [value]="zone" [selected]="zone === value()">{{ zone }}</option>
      }
    </select>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TimezoneSelectComponent {
  readonly value = input.required<string>();
  /** A showcase shows the zone and closes the picker, like every field beside it. */
  readonly disabled = input(false);

  readonly valueChange = output<string>();

  /**
   * The stored value is added to the list when the browser does not know it.
   * The two catalogues have different sources — `time/tzdata` server-side, the
   * browser's ICU here — so an alias present on one side only is possible, and a
   * select whose value matches no option renders blank and reports the wrong
   * zone on the next change.
   */
  protected readonly zones = computed(() => {
    const current = this.value();
    return current && !ZONES.includes(current) ? [current, ...ZONES] : ZONES;
  });
}
