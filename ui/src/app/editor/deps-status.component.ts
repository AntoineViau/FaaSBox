import { ChangeDetectionStrategy, Component, input } from '@angular/core';

import type { DepsStatus } from '@/models/faasbox-function.model';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * Where the dependency install stands, shown above the package.json editor.
 *
 * It lives inside the tab panel rather than in the permanent banner area: the
 * install state is what one comes to look at when looking at dependencies, and
 * a failure carries a preformatted terminal output that needs room a banner
 * spanning every tab cannot give it.
 */
@Component({
  selector: 'app-deps-status',
  standalone: true,
  imports: [ZardIconComponent],
  template: `
    @switch (status()) {
      @case ('pending') {
        <div class="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-muted-foreground">
          <z-icon zType="clock" class="h-4 w-4 shrink-0" />
          <span>Install queued.</span>
        </div>
      }
      @case ('installing') {
        <div class="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-muted-foreground">
          <z-icon zType="loader-circle" class="h-4 w-4 shrink-0 animate-spin" />
          <span>Installing dependencies…</span>
        </div>
      }
      @case ('ready') {
        <div class="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-green-600 dark:text-green-500">
          <z-icon zType="circle-check" class="h-4 w-4 shrink-0" />
          <span>Dependencies installed.</span>
        </div>
      }
      @case ('error') {
        <div class="border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-xs">
          <div class="flex items-center gap-2 text-destructive">
            <z-icon zType="circle-x" class="h-4 w-4 shrink-0" />
            <span class="font-medium">Dependency install failed.</span>
          </div>
          @if (error()) {
            <!-- Terminal output: preformatted, and bounded so a long failure
                 scrolls on its own instead of pushing the editor off screen. -->
            <pre
              class="mt-1.5 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-destructive/10 p-2 font-mono text-destructive"
              >{{ error() }}</pre
            >
          }
        </div>
      }
    }
  `,
  styles: `
    :host {
      display: block;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DepsStatusComponent {
  /** An empty status renders nothing: the function declares no dependencies. */
  readonly status = input.required<DepsStatus>();
  readonly error = input<string>('');
}
