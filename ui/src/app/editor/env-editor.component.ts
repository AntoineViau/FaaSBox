import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
} from '@angular/core';
import { firstValueFrom } from 'rxjs';

import { FunctionsService } from '@/editor/functions.service';
import { FunctionsStore } from '@/editor/functions.store';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

type Pair = { key: string; value: string };

/** `KEY=value` is built by joining on the first `=`, so neither can appear in a key. */
const INVALID_KEY = /[\s=]/;

@Component({
  selector: 'app-env-editor',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent, ZardInputDirective],
  template: `
    <div class="p-4">
      <div class="mb-3 flex items-start gap-2">
        <p class="flex-1 text-xs text-muted-foreground">
          Secret environment variables, encrypted when saved. Saving replaces the whole set: a
          variable removed here is removed from the function.
        </p>
        <button z-button zType="outline" zSize="sm" (click)="revealed.set(!revealed())">
          <z-icon zType="eye" class="mr-1.5 h-4 w-4" />
          {{ revealed() ? 'Hide' : 'Reveal' }}
        </button>
      </div>

      @if (error()) {
        <p class="mb-3 text-xs text-destructive">{{ error() }}</p>
      }

      @for (pair of pairs(); track $index) {
        <div class="mb-2 flex items-center gap-2">
          <input
            z-input
            type="text"
            placeholder="KEY"
            class="h-8 flex-1 font-mono text-sm"
            [value]="pair.key"
            (input)="setKey($index, $any($event.target).value)"
          />
          <input
            z-input
            [type]="revealed() ? 'text' : 'password'"
            placeholder="value"
            class="h-8 flex-[2] font-mono text-sm"
            [value]="pair.value"
            (input)="setValue($index, $any($event.target).value)"
          />
          <button z-button zType="ghost" zSize="icon" class="h-8 w-8" (click)="remove($index)">
            <z-icon zType="trash" class="h-3.5 w-3.5 text-destructive" />
          </button>
        </div>
      } @empty {
        <p class="mb-2 text-xs text-muted-foreground">No variable yet.</p>
      }

      <div class="mt-3 flex items-center gap-2">
        <button z-button zType="outline" zSize="sm" [disabled]="!loaded()" (click)="add()">
          <z-icon zType="plus" class="mr-1.5 h-4 w-4" />
          Add
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
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EnvEditorComponent {
  private readonly functionsService = inject(FunctionsService);
  private readonly store = inject(FunctionsStore);

  readonly functionId = input.required<string>();

  protected readonly pairs = signal<Pair[]>([]);
  protected readonly revealed = signal(false);
  protected readonly error = signal('');
  protected readonly saving = signal(false);

  private readonly savedSnapshot = signal('');
  /** False until the stored variables have been read once, successfully. */
  protected readonly loaded = signal(false);

  readonly isDirty = computed(
    () => this.loaded() && snapshot(this.pairs()) !== this.savedSnapshot(),
  );

  protected readonly canSave = computed(() => this.loaded() && this.isDirty() && !this.saving());

  constructor() {
    // Keyed on the id: renaming the open function must not discard the pairs
    // being edited, and the route takes either spelling.
    effect(() => void this.load(this.functionId()));
  }

  private async load(functionId: string): Promise<void> {
    this.loaded.set(false);
    this.error.set('');
    this.revealed.set(false);
    try {
      const env = await firstValueFrom(this.functionsService.getEnv(functionId));
      // A slow answer must not land on the function the user switched to.
      if (this.functionId() !== functionId) return;
      const pairs = Object.entries(env).map(([key, value]) => ({ key, value }));
      this.pairs.set(pairs);
      this.savedSnapshot.set(snapshot(pairs));
      this.loaded.set(true);
    } catch {
      if (this.functionId() !== functionId) return;
      this.pairs.set([]);
      // Saving stays disabled: replacing variables that could not be read would
      // drop the ones the user never saw.
      this.error.set('Could not read the current variables. They are left untouched.');
    }
  }

  protected add(): void {
    this.pairs.update((list) => [...list, { key: '', value: '' }]);
  }

  protected remove(index: number): void {
    this.pairs.update((list) => list.filter((_, i) => i !== index));
  }

  protected setKey(index: number, key: string): void {
    this.pairs.update((list) => list.map((p, i) => (i === index ? { ...p, key } : p)));
  }

  protected setValue(index: number, value: string): void {
    this.pairs.update((list) => list.map((p, i) => (i === index ? { ...p, value } : p)));
  }

  protected async save(): Promise<void> {
    const env: Record<string, string> = {};
    for (const pair of this.pairs()) {
      const key = pair.key.trim();
      // An untouched row added by mistake is dropped rather than refused.
      if (!key) continue;
      if (INVALID_KEY.test(key)) {
        this.error.set(`"${key}" is not a usable name: no spaces, no "=".`);
        return;
      }
      if (key in env) {
        this.error.set(`"${key}" appears twice. Keeping both would silently drop one.`);
        return;
      }
      env[key] = pair.value;
    }

    this.error.set('');
    this.saving.set(true);
    try {
      // An empty object is meaningful: the server reads it as "remove them all".
      await this.store.updateFunction(this.functionId(), { plainEnv: env });
      this.savedSnapshot.set(snapshot(this.pairs()));
    } catch (e) {
      this.error.set(`Failed to save: ${(e as Error).message}`);
    } finally {
      this.saving.set(false);
    }
  }
}

/** Order counts as content here: reordering rows reads as a change, which is harmless. */
function snapshot(pairs: Pair[]): string {
  return JSON.stringify(pairs);
}
