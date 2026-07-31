import {
  ChangeDetectionStrategy,
  Component,
  effect,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import { firstValueFrom } from 'rxjs';

import type { FaasboxCronJob } from '@/models/faasbox-cron-job.model';
import { CronService } from '@/editor/cron.service';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

const CRON_PRESETS: Record<string, string> = {
  '* * * * *': 'Every minute',
  '*/5 * * * *': 'Every 5 minutes',
  '*/10 * * * *': 'Every 10 minutes',
  '*/15 * * * *': 'Every 15 minutes',
  '*/30 * * * *': 'Every 30 minutes',
  '0 * * * *': 'Every hour',
  '0 */2 * * *': 'Every 2 hours',
  '0 */6 * * *': 'Every 6 hours',
  '0 */12 * * *': 'Every 12 hours',
  '0 0 * * *': 'Every day at midnight',
  '0 6 * * *': 'Every day at 6:00 AM',
  '0 12 * * *': 'Every day at noon',
  '0 0 * * 0': 'Every Sunday at midnight',
  '0 0 * * 1': 'Every Monday at midnight',
  '0 0 1 * *': 'First day of every month',
};

@Component({
  selector: 'app-cron-editor',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent, ZardInputDirective],
  template: `
    <div class="flex h-full flex-col overflow-y-auto p-4">
      @if (isLoading()) {
        <p class="text-sm text-muted-foreground">Loading triggers...</p>
      } @else {
        @if (crons().length === 0) {
          <p class="mb-4 text-sm text-muted-foreground">No triggers configured for this function.</p>
        }

        @for (cron of crons(); track cron.id) {
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
                    [value]="cron.name"
                    (change)="updateField(cron, 'name', $any($event.target).value)"
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
                    [value]="cron.schedule"
                    (change)="updateField(cron, 'schedule', $any($event.target).value)"
                  />
                  <p class="mt-0.5 text-xs text-muted-foreground">
                    {{ describeSchedule(cron.schedule) }}
                  </p>
                </div>

                <!-- Payload -->
                <div>
                  <label class="mb-1 block text-xs text-muted-foreground">Payload (JSON)</label>
                  <textarea
                    z-input
                    rows="2"
                    class="font-mono text-sm"
                    placeholder="{}"
                    [value]="formatPayload(cron.payload)"
                    (change)="updatePayload(cron, $any($event.target).value)"
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
                      value="{{ cron.maxQueue }}"
                      (change)="updateMaxQueue(cron, $any($event.target))"
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
                    [checked]="cron.active"
                    (change)="updateField(cron, 'active', $any($event.target).checked)"
                    class="h-4 w-4 accent-primary"
                  />
                  <span class="text-xs text-muted-foreground">Active</span>
                </label>
                <button
                  z-button
                  zType="ghost"
                  zSize="icon"
                  class="h-7 w-7"
                  (click)="deleteCron(cron)"
                >
                  <z-icon zType="trash" class="h-3.5 w-3.5 text-destructive" />
                </button>
              </div>
            </div>
          </div>
        }

        <button
          z-button
          zType="outline"
          zSize="sm"
          class="self-start"
          (click)="addCron()"
        >
          <z-icon zType="plus" class="mr-1.5 h-4 w-4" />
          Add trigger
        </button>
      }
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CronEditorComponent {
  private readonly cronService = inject(CronService);

  readonly functionName = input.required<string>();
  readonly cronCountChange = output<void>();

  protected readonly crons = signal<FaasboxCronJob[]>([]);
  protected readonly isLoading = signal(false);

  constructor() {
    effect(() => {
      const name = this.functionName();
      if (name) {
        this.loadCrons(name);
      }
    });
  }

  private async loadCrons(functionName: string): Promise<void> {
    this.isLoading.set(true);
    try {
      const res = await firstValueFrom(this.cronService.list(functionName));
      this.crons.set(res.items);
    } finally {
      this.isLoading.set(false);
    }
  }

  protected async addCron(): Promise<void> {
    const fnName = this.functionName();
    const existing = this.crons().length;
    const created = await firstValueFrom(
      this.cronService.create({
        name: existing === 0 ? fnName : `${fnName}-${existing + 1}`,
        schedule: '0 * * * *',
        functionName: fnName,
        payload: {},
        active: true,
        maxQueue: 0,
      }),
    );
    this.crons.update((list) => [created, ...list]);
    this.cronCountChange.emit();
  }

  protected async updateField(cron: FaasboxCronJob, field: string, value: unknown): Promise<void> {
    const updated = await firstValueFrom(this.cronService.update(cron.id, { [field]: value }));
    this.crons.update((list) => list.map((c) => (c.id === cron.id ? updated : c)));
    if (field === 'active') {
      this.cronCountChange.emit();
    }
  }

  protected async updateMaxQueue(cron: FaasboxCronJob, input: HTMLInputElement): Promise<void> {
    // The DOM yields a string; maxQueue is a PocketBase NumberField. Empty, invalid
    // and negative inputs all collapse to 0, which means "no limit" server-side.
    const parsed = Number.parseInt(input.value, 10);
    const value = Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
    // Re-sync the field when the typed text was normalized: rebinding alone would
    // not repaint it if the stored value did not change (e.g. 0 -> "-5" -> 0).
    if (input.value !== String(value)) {
      input.value = String(value);
    }
    await this.updateField(cron, 'maxQueue', value);
  }

  protected async updatePayload(cron: FaasboxCronJob, text: string): Promise<void> {
    let parsed: unknown;
    try {
      parsed = text.trim() ? JSON.parse(text) : {};
    } catch {
      return; // ignore invalid JSON
    }
    await this.updateField(cron, 'payload', parsed);
  }

  protected async deleteCron(cron: FaasboxCronJob): Promise<void> {
    await firstValueFrom(this.cronService.delete(cron.id));
    this.crons.update((list) => list.filter((c) => c.id !== cron.id));
    this.cronCountChange.emit();
  }

  protected formatPayload(payload: unknown): string {
    if (payload == null) return '';
    if (typeof payload === 'string') return payload;
    return JSON.stringify(payload, null, 2);
  }

  protected describeSchedule(schedule: string): string {
    const trimmed = schedule.trim();
    if (!trimmed) return '';
    const preset = CRON_PRESETS[trimmed];
    if (preset) return preset;
    return 'Custom schedule — check crontab.guru';
  }
}
