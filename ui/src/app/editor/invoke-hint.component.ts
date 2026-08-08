import { ChangeDetectionStrategy, Component, computed, HostListener, input, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * How to call the open function from outside the editor.
 *
 * The route was only written in docs/, so learning to invoke what one had just
 * written meant leaving the product. The banner names the route in place, and
 * the dialog carries the whole call, ready to paste.
 *
 * The dialog is written here rather than in shared/: there is no shared dialog
 * component to reuse — the editor still falls back on the native confirm() —
 * and vendoring one for a single caller would be the larger commitment.
 */
@Component({
  selector: 'app-invoke-hint',
  standalone: true,
  imports: [RouterLink, ZardButtonComponent, ZardIconComponent],
  template: `
    <div class="flex items-center gap-3 border-b border-border px-4 py-1.5 text-xs">
      <code class="font-mono text-muted-foreground">POST /invoke/{{ name() }}</code>
      <button
        type="button"
        class="text-muted-foreground underline underline-offset-2 hover:text-foreground"
        (click)="open()"
      >
        Example with curl
      </button>
    </div>

    @if (dialogOpen()) {
      <div
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
        role="dialog"
        aria-modal="true"
        aria-labelledby="invoke-hint-title"
      >
        <div class="w-full max-w-2xl rounded-lg border border-border bg-background p-4 shadow-lg">
          <div class="flex items-start justify-between gap-4">
            <h2 id="invoke-hint-title" class="text-sm font-semibold">Invoke {{ name() }}</h2>
            <button z-button zType="ghost" zSize="icon" class="h-7 w-7" (click)="close()">
              <z-icon zType="x" class="h-4 w-4" />
            </button>
          </div>
          <pre
            class="mt-3 overflow-x-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs"
            >{{ curlExample() }}</pre
          >
          <p class="mt-3 text-xs text-muted-foreground">
            <code class="font-mono">fbx_your_key_here</code> is a placeholder: the value of a key is
            only ever shown once, when it is created. Create one on the
            <a routerLink="/keys" class="underline underline-offset-2 hover:text-foreground">API keys</a>
            page.
          </p>
          <div class="mt-4 flex justify-end">
            <button z-button zType="secondary" zSize="sm" (click)="close()">Close</button>
          </div>
        </div>
      </div>
    }
  `,
  styles: `
    :host {
      display: block;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class InvokeHintComponent {
  /** The saved name of the open function — the one /invoke answers to today. */
  readonly name = input.required<string>();

  protected readonly dialogOpen = signal(false);

  /**
   * The origin is read from the browser, not hardcoded: the example has to work
   * as it stands on the instance it is shown from, not on a development host.
   */
  protected readonly curlExample = computed(
    () =>
      `curl -X POST ${window.location.origin}/invoke/${this.name()} \\
  -H "X-API-Key: fbx_your_key_here" \\
  -H "Content-Type: application/json" \\
  -d '{"hello":"world"}'`,
  );

  protected open(): void {
    this.dialogOpen.set(true);
  }

  protected close(): void {
    this.dialogOpen.set(false);
  }

  // On the document rather than the host: the dialog is a modal overlay, and
  // Escape has to reach it wherever the focus sits.
  @HostListener('document:keydown.escape')
  protected onEscape(): void {
    this.close();
  }
}
