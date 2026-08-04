import { ChangeDetectionStrategy, Component, effect, inject, input, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';

import type { FaasboxLog } from '@/models/faasbox-log.model';
import { LogsService } from '@/editor/logs.service';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

@Component({
  selector: 'app-log-viewer',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent],
  template: `
    <div class="flex h-full flex-col">
      <!-- Toolbar -->
      <div class="flex items-center gap-2 border-b border-border px-3 py-1.5">
        <span class="text-xs font-semibold uppercase tracking-wider text-muted-foreground"
          >Logs</span
        >
        <span class="rounded-full bg-muted px-1.5 text-xs text-muted-foreground">{{
          logs().length
        }}</span>
        <div class="flex-1"></div>
        <button z-button zType="ghost" zSize="sm" (click)="refresh()">
          <z-icon zType="refresh-cw" class="h-3.5 w-3.5" />
        </button>
      </div>

      <!-- Log list -->
      <div class="flex-1 overflow-y-auto">
        @if (isLoading()) {
          <div class="flex items-center justify-center py-4">
            <z-icon zType="loader-circle" class="h-4 w-4 animate-spin text-muted-foreground" />
          </div>
        } @else if (logs().length === 0) {
          <div class="flex items-center justify-center py-4 text-xs text-muted-foreground">
            No logs yet.
          </div>
        } @else {
          @for (log of logs(); track log.id) {
            <div
              class="cursor-pointer border-b border-border/50 px-3 py-1.5 transition-colors hover:bg-accent/30"
              (click)="toggleExpand(log.id)"
            >
              <div class="flex items-center gap-2 text-xs">
                @switch (log.status) {
                  @case ('success') {
                    <z-icon zType="circle-check" class="h-3.5 w-3.5 shrink-0 text-green-500" />
                  }
                  @case ('error') {
                    <z-icon zType="circle-x" class="h-3.5 w-3.5 shrink-0 text-destructive" />
                  }
                  @case ('timeout') {
                    <z-icon zType="circle-alert" class="h-3.5 w-3.5 shrink-0 text-orange-500" />
                  }
                  @case ('missed') {
                    <z-icon zType="clock" class="h-3.5 w-3.5 shrink-0 text-yellow-500" />
                  }
                }
                <span class="rounded bg-muted px-1 py-0.5 text-[10px] font-medium uppercase">
                  {{ log.trigger }}
                </span>
                <span class="text-muted-foreground">{{ log.duration }}ms</span>
                <div class="flex-1"></div>
                <span class="text-muted-foreground" [title]="log.created">
                  {{ timeAgo(log.created) }}
                </span>
                <z-icon
                  [zType]="expandedId() === log.id ? 'chevron-up' : 'chevron-down'"
                  class="h-3 w-3 text-muted-foreground"
                />
              </div>
            </div>

            @if (expandedId() === log.id) {
              <div class="space-y-2 border-b border-border/50 bg-muted/30 px-3 py-2 text-xs">
                @if (log.stdout) {
                  <div>
                    <div class="mb-0.5 text-muted-foreground">stdout</div>
                    <pre class="overflow-auto rounded bg-muted p-2 font-mono">{{ log.stdout }}</pre>
                  </div>
                }
                @if (log.stderr) {
                  <div>
                    <div class="mb-0.5 text-muted-foreground">stderr</div>
                    <pre
                      class="overflow-auto rounded bg-destructive/10 p-2 font-mono text-destructive"
                      >{{ log.stderr }}</pre
                    >
                  </div>
                }
                @if (log.requestPayload) {
                  <div>
                    <div class="mb-0.5 text-muted-foreground">payload</div>
                    <pre class="overflow-auto rounded bg-muted p-2 font-mono">{{
                      formatJson(log.requestPayload)
                    }}</pre>
                  </div>
                }
                <div class="text-muted-foreground">Exit code: {{ log.exitCode }}</div>
              </div>
            }
          }
        }
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LogViewerComponent {
  private readonly logsService = inject(LogsService);

  readonly functionName = input.required<string>();

  protected readonly logs = signal<FaasboxLog[]>([]);
  protected readonly isLoading = signal(false);
  protected readonly expandedId = signal<string | null>(null);

  constructor() {
    effect((onCleanup) => {
      const name = this.functionName();
      this.loadLogs(name);
      const unsubscribe = this.logsService.subscribe(
        name,
        (log) => {
          this.logs.update((current) => [log, ...current].slice(0, 50));
        },
        // Entries written while the stream was down never arrived; appending to
        // the list from there on would leave a hole nothing ever fills.
        () => this.loadLogs(name),
      );
      onCleanup(() => unsubscribe());
    });
  }

  protected async loadLogs(functionName: string): Promise<void> {
    this.isLoading.set(true);
    try {
      const res = await firstValueFrom(this.logsService.list(functionName));
      this.logs.set(res.items);
    } catch {
      this.logs.set([]);
    } finally {
      this.isLoading.set(false);
    }
  }

  protected refresh(): void {
    this.loadLogs(this.functionName());
  }

  protected toggleExpand(id: string): void {
    this.expandedId.update((current) => (current === id ? null : id));
  }

  protected timeAgo(dateString: string): string {
    if (!dateString) return '';
    const seconds = Math.floor(
      (Date.now() - new Date(dateString.replace(' ', 'T')).getTime()) / 1000,
    );
    if (isNaN(seconds)) return dateString;
    if (seconds < 60) return 'just now';
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    return `${days}d ago`;
  }

  protected formatJson(value: unknown): string {
    if (typeof value === 'string') return value;
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }
}
