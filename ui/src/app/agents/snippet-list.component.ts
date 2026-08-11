import { ChangeDetectionStrategy, Component, input, signal } from '@angular/core';

import type { AgentClient } from '@/agents/agent-snippets';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * One integration snippet per client, each with a button that copies it.
 *
 * A component rather than markup repeated in the page, because the page shows
 * two of these — one per way of authenticating — and the "Copied" confirmation
 * has to be per card. Two instances hold two independent signals, so the same
 * client id appearing in both lists cannot light up the wrong card.
 */
@Component({
  selector: 'app-snippet-list',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent],
  template: `
    @for (client of clients(); track client.id) {
      <div class="mb-3 rounded-lg border border-border p-3">
        <div class="flex items-baseline justify-between gap-3">
          <div class="flex items-baseline gap-2">
            <span class="text-sm font-semibold">{{ client.name }}</span>
            <span class="text-xs text-muted-foreground">{{ client.target }}</span>
          </div>
          <button z-button zType="ghost" zSize="sm" (click)="copy(client)">
            <z-icon
              [zType]="copied() === client.id ? 'check' : 'copy'"
              class="mr-1.5 h-3.5 w-3.5"
            />
            {{ copied() === client.id ? 'Copied' : 'Copy' }}
          </button>
        </div>
        <pre
          class="mt-2 overflow-x-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs"
          >{{ client.snippet }}</pre
        >
      </div>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SnippetListComponent {
  readonly clients = input.required<readonly AgentClient[]>();

  /** Which card said "Copied" last, so the confirmation is per card. */
  protected readonly copied = signal('');

  protected async copy(client: AgentClient): Promise<void> {
    try {
      await navigator.clipboard.writeText(client.snippet);
      this.copied.set(client.id);
    } catch {
      // Refused permission, or an insecure origin. The snippet is on screen and
      // selectable either way, so there is nothing to report and nothing to fix.
      this.copied.set('');
    }
  }
}
