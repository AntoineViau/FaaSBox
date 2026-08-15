import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import { firstValueFrom } from 'rxjs';

import type { FaasboxCronJob } from '@/models/faasbox-cron-job.model';
import { CronService } from '@/editor/cron.service';
import { CronTriggerCardComponent, type CronRow } from '@/editor/cron-trigger-card.component';
import { errorText } from '@/editor/error-text';
import { ZardAlertComponent } from '@shared/components/alert';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * The Triggers panel.
 *
 * Nothing here is written as it is typed: adding, editing and deleting all stay
 * on screen until Save, which reconciles the list with the database. Immediate
 * writes left a refused schedule displayed as if it had been accepted.
 *
 * Slightly past the 300-line guideline, deliberately: the overshoot is the
 * comments carrying the save/switch constraints, and the helpers at the bottom
 * read nothing but a row.
 */
@Component({
  selector: 'app-cron-editor',
  standalone: true,
  imports: [CronTriggerCardComponent, ZardAlertComponent, ZardButtonComponent, ZardIconComponent],
  template: `
    <div class="flex h-full flex-col overflow-y-auto p-4">
      @if (errorMessage()) {
        <z-alert class="mb-4" zType="destructive" zTitle="Error" [zDescription]="errorMessage()" />
      }

      @if (isLoading()) {
        <p class="text-sm text-muted-foreground">Loading triggers...</p>
      } @else {
        @if (rows().length === 0) {
          <p class="mb-4 text-sm text-muted-foreground">No triggers configured for this function.</p>
        }

        @for (row of rows(); track row.key) {
          <app-cron-trigger-card
            [row]="row"
            (rowChange)="patch(row.key, $event)"
            (removeRow)="remove(row.key)"
          />
        }

        <div class="flex items-center gap-2">
          <button z-button zType="outline" zSize="sm" (click)="add()">
            <z-icon zType="plus" class="mr-1.5 h-4 w-4" />
            Add trigger
          </button>
          <button z-button zType="default" zSize="sm" [disabled]="!canSave()" (click)="save()">
            <z-icon zType="save" class="mr-1.5 h-4 w-4" />
            Save
          </button>
          @if (saving()) {
            <span class="text-xs text-muted-foreground">Saving...</span>
          } @else if (isDirty()) {
            <span class="text-xs text-muted-foreground">(unsaved changes)</span>
          }
        </div>
      }
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CronEditorComponent {
  private readonly cronService = inject(CronService);

  /** What a trigger points at, and what the panel loads and writes on. */
  readonly functionId = input.required<string>();
  /** Only seeds the default name of a new trigger; never written as a reference. */
  readonly functionName = input.required<string>();
  readonly cronCountChange = output<void>();

  protected readonly rows = signal<CronRow[]>([]);
  protected readonly isLoading = signal(false);
  protected readonly saving = signal(false);
  protected readonly errorMessage = signal('');

  /**
   * What the database is known to hold, keyed by record id. Only rows that
   * exist there are listed: a row absent from it is a creation, and an entry
   * with no row left on screen is a deletion. Recomputing the work at save time
   * from these two lists is what keeps a delete-then-re-add from turning into a
   * removal followed by a different record.
   */
  private readonly saved = signal<Map<string, CronRow>>(new Map());

  private nextKey = 0;

  /**
   * Public because the editor asks before switching function: this panel keeps
   * its rows on screen until Save, so leaving it behind loses them silently.
   */
  readonly isDirty = computed(() => {
    const saved = this.saved();
    const rows = this.rows();
    if (rows.length !== saved.size) return true;
    return rows.some((row) => {
      const previous = saved.get(row.id);
      return !previous || !sameRow(previous, row);
    });
  });

  protected readonly canSave = computed(() => !this.saving() && this.isDirty());

  constructor() {
    // Keyed on the id: renaming the open function moves no trigger, so the panel
    // has nothing to reload and no reason to drop what is on screen.
    effect(() => {
      const id = this.functionId();
      if (id) {
        this.load(id);
      }
    });
  }

  private async load(functionId: string): Promise<void> {
    this.isLoading.set(true);
    this.errorMessage.set('');
    try {
      const res = await firstValueFrom(this.cronService.list(functionId));
      // A slow answer must not land on the function the user switched to.
      if (this.functionId() !== functionId) return;
      this.rows.set(byNameDescending(res.items.map((item) => this.toRow(item))));
      this.snapshot();
    } catch (e) {
      this.errorMessage.set(`Failed to load the triggers: ${errorText(e)}`);
    } finally {
      this.isLoading.set(false);
    }
  }

  protected add(): void {
    const fnName = this.functionName();
    const existing = this.rows().length;
    // On screen only: nothing reaches the database until Save.
    this.rows.update((list) => [
      {
        key: this.nextKey++,
        id: '',
        name: existing === 0 ? fnName : `${fnName}-${existing + 1}`,
        schedule: '0 * * * *',
        payload: '{}',
        active: true,
        maxQueue: 0,
      },
      ...list,
    ]);
  }

  protected patch(key: number, changes: Partial<CronRow>): void {
    this.rows.update((list) => list.map((row) => (row.key === key ? { ...row, ...changes } : row)));
  }

  protected remove(key: number): void {
    this.rows.update((list) => list.filter((row) => row.key !== key));
  }

  protected async save(): Promise<void> {
    // Captured once. The writes below are interleaved with awaits, and nothing
    // stops the user from picking another function in the sidebar meanwhile:
    // re-reading the input would file the rest of the rows under it.
    const functionId = this.functionId();
    const rows = this.rows();

    // Every payload is parsed first: a malformed document stops the whole save
    // instead of writing half of it and reporting the rest.
    const payloads = new Map<number, unknown>();
    for (const row of rows) {
      const text = row.payload.trim();
      try {
        payloads.set(row.key, text ? JSON.parse(text) : {});
      } catch {
        this.errorMessage.set(
          `"${label(row)}": the payload is not valid JSON. Nothing was saved.`,
        );
        return;
      }
    }

    this.errorMessage.set('');
    this.saving.set(true);

    const saved = new Map(this.saved());
    const failures: string[] = [];
    let written = false;

    // Deletions first: an entry whose row left the screen has to go.
    for (const [id, previous] of this.saved()) {
      if (rows.some((row) => row.id === id)) continue;
      try {
        await firstValueFrom(this.cronService.delete(id));
        saved.delete(id);
        written = true;
      } catch (e) {
        failures.push(`"${label(previous)}": ${errorText(e)}`);
      }
    }

    for (const row of rows) {
      const previous = row.id ? saved.get(row.id) : undefined;
      if (previous && sameRow(previous, row)) continue;
      const data: Partial<FaasboxCronJob> = {
        name: row.name,
        schedule: row.schedule,
        function: functionId,
        payload: payloads.get(row.key),
        active: row.active,
        maxQueue: row.maxQueue,
      };
      try {
        const record = row.id
          ? await firstValueFrom(this.cronService.update(row.id, data))
          : await firstValueFrom(this.cronService.create(data));
        // A creation only becomes an update on the next save once its row
        // carries the id the server just handed back.
        this.patch(row.key, { id: record.id });
        saved.set(record.id, { ...row, id: record.id });
        written = true;
      } catch (e) {
        // The other triggers stay written; this one keeps what was typed, so it
        // can be corrected and saved again.
        failures.push(`"${label(row)}": ${errorText(e)}`);
      }
    }

    this.saving.set(false);

    // Never before: the sidebar icon must not announce a schedule the database
    // refused. Not guarded either: the icons are recomputed for every function
    // at once, and the writes that just landed are real whichever function the
    // panel is showing by now.
    if (written) {
      this.cronCountChange.emit();
    }

    // Past here everything describes the function this save started on. If the
    // panel has moved since, load() has already filled it with another one —
    // its snapshot is not ours to overwrite, and its errors are not ours to
    // report. The writes above stand; only their echo is dropped.
    if (this.functionId() !== functionId) return;

    this.saved.set(saved);

    if (failures.length) {
      this.errorMessage.set(`Failed to save — ${failures.join(' — ')}`);
    }
  }

  private toRow(job: FaasboxCronJob): CronRow {
    return {
      key: this.nextKey++,
      id: job.id,
      name: job.name,
      schedule: job.schedule,
      payload: formatPayload(job.payload),
      active: job.active,
      maxQueue: job.maxQueue ?? 0,
    };
  }

  /** Records what the database now holds, which is what isDirty compares against. */
  private snapshot(): void {
    this.saved.set(new Map(this.rows().filter((row) => row.id).map((row) => [row.id, { ...row }])));
  }
}

/**
 * Puts the triggers back in the order the server used to settle.
 *
 * It cannot settle it any more: the name is encrypted at rest, and sorting on a
 * ciphertext orders noise. The comparison is on code units rather than through
 * `localeCompare`, which is what SQLite's default collation did — the point is
 * the same order as before, not a better one.
 */
function byNameDescending(rows: CronRow[]): CronRow[] {
  return rows.sort((a, b) => (a.name < b.name ? 1 : a.name > b.name ? -1 : 0));
}

/** Compares what is stored; the on-screen key and the record id are not part of it. */
function sameRow(a: CronRow, b: CronRow): boolean {
  return (
    a.name === b.name &&
    a.schedule === b.schedule &&
    a.payload === b.payload &&
    a.active === b.active &&
    a.maxQueue === b.maxQueue
  );
}

/** What names a trigger in a message: its name, or its schedule for an unnamed one. */
function label(row: CronRow): string {
  return row.name.trim() || row.schedule.trim() || 'unnamed trigger';
}

function formatPayload(payload: unknown): string {
  if (payload == null) return '';
  if (typeof payload === 'string') return payload;
  return JSON.stringify(payload, null, 2);
}

