import { ChangeDetectionStrategy, Component, input } from '@angular/core';

import { ZardIconComponent } from '@shared/components/icon';

/**
 * The strip that says this instance is a showcase.
 *
 * It is placed once, above the router outlet, because there is no shared page
 * shell and it has to show on the sign-in page as well as in the editor.
 * Presentational: the mode arrives as an input.
 *
 * **A solid band, not a tint.** Everything else on the page is a surface the
 * eye is meant to work on; this is the one thing that has to be read before any
 * of it. A ten-percent wash of the accent colour reads as one more panel, and a
 * visitor who missed it wonders why every button is dead. The fill is the same
 * in both themes on purpose — a banner that adapts to its surroundings is a
 * banner that blends into them.
 */
@Component({
  selector: 'app-demo-banner',
  standalone: true,
  imports: [ZardIconComponent],
  template: `
    @if (demoMode()) {
      <div
        class="flex shrink-0 flex-wrap items-center justify-center gap-x-3 gap-y-0.5 bg-yellow-400 px-4 py-2.5 text-center text-yellow-950 dark:bg-yellow-500"
      >
        <span class="flex items-center gap-2">
          <z-icon zType="eye" class="h-5 w-5 shrink-0" />
          <span class="text-base font-bold tracking-widest">DEMO MODE</span>
        </span>
        <span class="text-sm font-medium text-yellow-950/80">
          everything is visible, nothing runs and nothing can be changed.
        </span>
      </div>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DemoBannerComponent {
  readonly demoMode = input(false);
}
