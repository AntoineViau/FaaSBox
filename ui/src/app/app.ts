import { ChangeDetectionStrategy, Component, inject } from '@angular/core';
import { RouterOutlet } from '@angular/router';

import { DemoBannerComponent } from '@/instance/demo-banner.component';
import { InstanceService } from '@/instance/instance.service';

/**
 * The shell, and the only place common to every page: there is no shared page
 * layout, so the demo banner has to live here to show on the sign-in page as
 * well as in the editor.
 *
 * The routed pages fill the box below the banner rather than the viewport,
 * which is what keeps the banner from pushing them off the bottom of the
 * screen. It is also why they measure themselves in percent and not in `vh`.
 */
@Component({
  selector: 'app-root',
  imports: [DemoBannerComponent, RouterOutlet],
  template: `
    <div class="flex h-screen flex-col">
      <app-demo-banner [demoMode]="demoMode()" />
      <div class="min-h-0 flex-1">
        <router-outlet />
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App {
  private readonly instance = inject(InstanceService);

  protected readonly demoMode = this.instance.demoMode;
}
