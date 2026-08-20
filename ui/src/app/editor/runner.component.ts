import {
  ChangeDetectionStrategy,
  Component,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import { firstValueFrom } from 'rxjs';

import type { InvocationResult } from '@/models/invocation-result.model';
import { FunctionsService } from '@/editor/functions.service';
import { CodeEditorComponent } from '@/editor/code-editor.component';
import { DEMO_MODE_HINT } from '@/instance/instance.service';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

@Component({
  selector: 'app-runner',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent, CodeEditorComponent],
  template: `
    <div class="flex h-full flex-col">
      <!-- Toolbar -->
      <div class="flex items-center gap-2 border-b border-border px-3 py-1.5">
        <span class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Runner</span>
        <div class="flex-1"></div>
        @if (busy()) {
          <span class="flex items-center text-xs text-muted-foreground">
            <z-icon zType="loader-circle" class="mr-1.5 h-3.5 w-3.5 animate-spin" />
            Running...
          </span>
        }
        <!-- The hint goes on the wrapper: a disabled button gets no hover
             event, so a title placed on it would never show. -->
        <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
          <button
            z-button
            [zType]="dirty() ? 'outline' : 'default'"
            zSize="sm"
            [disabled]="busy() || !functionName() || demoMode()"
            (click)="run.emit()"
          >
            <z-icon zType="zap" class="mr-1.5 h-4 w-4" />
            Run
          </button>
        </span>
        @if (dirty()) {
          <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
            <button
              z-button
              zType="default"
              zSize="sm"
              [disabled]="busy() || !functionName() || demoMode()"
              (click)="saveAndRun.emit()"
            >
              <z-icon zType="save" class="mr-1.5 h-4 w-4" />
              Save and run
            </button>
          </span>
        }
      </div>

      <!-- Content: payload + result side by side -->
      <div class="flex flex-1 overflow-hidden">
        <!-- Payload -->
        <div class="flex w-1/3 flex-col border-r border-border">
          <div class="px-3 py-1 text-xs text-muted-foreground">Payload (JSON)</div>
          <div class="flex-1 overflow-auto">
            <app-code-editor
              [content]="payload()"
              language="json"
              [readOnly]="demoMode()"
              (contentChange)="payload.set($event)"
            />
          </div>
        </div>

        <!-- Result -->
        <div class="flex flex-1 flex-col overflow-auto">
          @if (lastResult(); as r) {
            <div class="flex flex-col gap-2 p-3 text-sm">
              <!-- Status line -->
              <div class="flex items-center gap-2">
                @if (r.error) {
                  <z-icon zType="circle-x" class="h-4 w-4 text-destructive" />
                  <span class="font-medium text-destructive">Error</span>
                } @else {
                  <z-icon zType="circle-check" class="h-4 w-4 text-green-500" />
                  <span class="font-medium text-green-500">Success</span>
                }
                <span class="text-xs text-muted-foreground">{{ r.duration_ms }}ms</span>
                @if (r.truncated) {
                  <span class="text-xs text-yellow-500">(output truncated)</span>
                }
              </div>

              <!-- Result or error -->
              @if (r.result !== undefined) {
                <div>
                  <div class="mb-1 text-xs text-muted-foreground">Result</div>
                  <pre class="overflow-auto rounded-md bg-muted p-2 text-xs">{{ formatJson(r.result) }}</pre>
                </div>
              }
              @if (r.error) {
                <div>
                  <div class="mb-1 text-xs text-muted-foreground">Error</div>
                  <pre class="overflow-auto rounded-md bg-destructive/10 p-2 text-xs text-destructive">{{ r.error }}</pre>
                </div>
              }

              <!-- Stdout -->
              @if (r.stdout) {
                <div>
                  <div class="mb-1 text-xs text-muted-foreground">stdout</div>
                  <pre class="overflow-auto rounded-md bg-muted p-2 text-xs">{{ r.stdout }}</pre>
                </div>
              }

              <!-- Stderr -->
              @if (r.stderr) {
                <div>
                  <div class="mb-1 text-xs text-muted-foreground">stderr</div>
                  <pre class="overflow-auto rounded-md bg-yellow-500/10 p-2 text-xs text-yellow-500">{{ r.stderr }}</pre>
                </div>
              }
            </div>
          } @else {
            <div class="flex flex-1 items-center justify-center text-xs text-muted-foreground">
              Click "Run" to execute the function.
            </div>
          }
        </div>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunnerComponent {
  private readonly functionsService = inject(FunctionsService);

  readonly functionName = input.required<string>();
  /** Saving and running are one action, driven by the editor: it owns the flag. */
  readonly busy = input(false);
  /**
   * True while the buffer differs from what the server holds. The editor owns
   * it, like busy: the runner has no view on the edit buffer.
   */
  readonly dirty = input(false);
  /**
   * A showcase keeps the panel on screen — it is part of what the instance is
   * there to display — and closes the two buttons that would execute something.
   */
  readonly demoMode = input(false);

  /**
   * Asks the editor to call execute() straight away. /invoke runs the file on
   * disk, so this diagnoses the last saved version — which is the point when
   * nothing was edited.
   */
  readonly run = output<void>();

  /**
   * Asks the editor to save the buffer first, then call execute(). Only offered
   * while the two differ, so it disappears once there is nothing left to save.
   */
  readonly saveAndRun = output<void>();

  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  protected readonly payload = signal('{}');
  protected readonly lastResult = signal<InvocationResult | null>(null);

  /** Called by the editor once the save went through. */
  async execute(): Promise<void> {
    if (this.demoMode()) return;
    const name = this.functionName();
    if (!name) return;

    let parsed: unknown;
    try {
      parsed = JSON.parse(this.payload());
    } catch {
      alert('Invalid JSON payload.');
      return;
    }

    this.lastResult.set(null);

    try {
      const result = await firstValueFrom(this.functionsService.invoke(name, parsed));
      this.lastResult.set(result);
    } catch (e: any) {
      if (e.error && typeof e.error === 'object') {
        this.lastResult.set(e.error as InvocationResult);
      } else {
        this.lastResult.set({
          function: name,
          error: e.message ?? 'Unknown error',
          duration_ms: 0,
        });
      }
    }
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
