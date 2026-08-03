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
        <button
          z-button
          zType="default"
          zSize="sm"
          [disabled]="busy() || !functionName()"
          (click)="run.emit()"
        >
          @if (busy()) {
            <z-icon zType="loader-circle" class="mr-1.5 h-4 w-4 animate-spin" />
            Running...
          } @else {
            <z-icon zType="zap" class="mr-1.5 h-4 w-4" />
            Save and run
          }
        </button>
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
   * Asks the editor to save the buffer, then call execute(). /invoke runs the
   * file on disk, so running without saving first would diagnose code the user
   * is not looking at.
   */
  readonly run = output<void>();

  protected readonly payload = signal('{}');
  protected readonly lastResult = signal<InvocationResult | null>(null);

  /** Called by the editor once the save went through. */
  async execute(): Promise<void> {
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
