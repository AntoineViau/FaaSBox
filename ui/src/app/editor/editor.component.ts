import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  HostListener,
  inject,
  type OnInit,
  signal,
} from '@angular/core';
import { firstValueFrom } from 'rxjs';

import { AuthService } from '@/auth/auth.service';
import { CronService } from '@/editor/cron.service';
import { FunctionsStore } from '@/editor/functions.store';
import { CodeEditorComponent } from '@/editor/code-editor.component';
import { CronEditorComponent } from '@/editor/cron-editor.component';
import { LogViewerComponent } from '@/editor/log-viewer.component';
import { RunnerComponent } from '@/editor/runner.component';
import { SidebarComponent } from '@/editor/sidebar.component';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';
import { ZardTabGroupComponent, ZardTabComponent } from '@shared/components/tabs';

@Component({
  selector: 'app-editor',
  standalone: true,
  imports: [
    ZardButtonComponent,
    ZardIconComponent,
    ZardInputDirective,
    ZardTabGroupComponent,
    ZardTabComponent,
    CodeEditorComponent,
    CronEditorComponent,
    LogViewerComponent,
    RunnerComponent,
    SidebarComponent,
  ],
  template: `
    <div class="flex h-screen flex-col">
      <!-- Header -->
      <header class="flex items-center justify-between border-b border-border px-4 py-2">
        <div class="flex items-center gap-3">
          <h1 class="text-lg font-semibold">FaaSBox</h1>
          @if (isDirty()) {
            <span class="text-xs text-muted-foreground">(unsaved changes)</span>
          }
        </div>
        <div class="flex items-center gap-2">
          <button
            z-button
            zType="default"
            zSize="sm"
            [disabled]="!isDirty()"
            (click)="save()"
          >
            <z-icon zType="save" class="mr-1.5 h-4 w-4" />
            Save
          </button>
          <button
            z-button
            [zType]="showRunner() ? 'secondary' : 'outline'"
            zSize="sm"
            (click)="showRunner.set(!showRunner())"
          >
            <z-icon zType="terminal" class="mr-1.5 h-4 w-4" />
            Runner
          </button>
          <button
            z-button
            [zType]="showLogs() ? 'secondary' : 'outline'"
            zSize="sm"
            (click)="showLogs.set(!showLogs())"
          >
            <z-icon zType="scroll-text" class="mr-1.5 h-4 w-4" />
            Logs
          </button>
          <button z-button zType="outline" zSize="sm" (click)="logout()">
            <z-icon zType="log-out" class="mr-1.5 h-4 w-4" />
            Sign out
          </button>
        </div>
      </header>

      <!-- Main -->
      <div class="flex flex-1 overflow-hidden">
        <!-- Sidebar -->
        <div class="w-56 shrink-0 border-r border-border">
          <app-sidebar
            [functions]="store.sortedFunctions()"
            [selectedId]="store.selectedId()"
            [cronFunctions]="cronFunctions()"
            (select)="onSelectFunction($event)"
            (create)="onCreateFunction()"
            (deleteItem)="onDeleteFunction($event)"
          />
        </div>

        <!-- Editor area -->
        <div class="flex flex-1 flex-col overflow-hidden">
          @if (store.selectedFunction(); as fn) {
            <!-- Name field -->
            <div class="border-b border-border px-4 py-2">
              <input
                z-input
                type="text"
                placeholder="Function name"
                [value]="localName()"
                (input)="localName.set($any($event.target).value)"
                class="h-8 text-sm"
              />
            </div>

            <!-- Tabs -->
            <z-tab-group class="flex flex-1 flex-col overflow-hidden" zTabsPosition="top" [zShowArrow]="false">
              <z-tab label="Script">
                <div class="h-full">
                  <app-code-editor
                    [content]="localScript()"
                    language="typescript"
                    (contentChange)="localScript.set($event)"
                  />
                </div>
              </z-tab>
              <z-tab label="package.json">
                <div class="h-full">
                  <app-code-editor
                    [content]="localPackageJson()"
                    language="json"
                    (contentChange)="localPackageJson.set($event)"
                  />
                </div>
              </z-tab>
              <z-tab label="Triggers">
                <app-cron-editor
                  [functionName]="fn.name"
                  (cronCountChange)="loadCronFunctions()"
                />
              </z-tab>
              <z-tab label="Environment">
                <div class="p-4">
                  <p class="mb-2 text-xs text-muted-foreground">
                    JSON object of secret environment variables. Encrypted at save time.
                  </p>
                  <textarea
                    z-input
                    rows="10"
                    placeholder='{"MY_SECRET": "value"}'
                    [value]="localPlainEnv()"
                    (input)="localPlainEnv.set($any($event.target).value)"
                    class="font-mono text-sm"
                  ></textarea>
                </div>
              </z-tab>
            </z-tab-group>

            <!-- Runner panel -->
            @if (showRunner()) {
              <div class="h-64 shrink-0 border-t border-border">
                <app-runner [functionName]="fn.name" />
              </div>
            }

            <!-- Logs panel -->
            @if (showLogs()) {
              <div class="h-64 shrink-0 border-t border-border">
                <app-log-viewer [functionName]="fn.name" />
              </div>
            }
          } @else {
            <div class="flex flex-1 items-center justify-center">
              <p class="text-muted-foreground">Select or create a function to start editing.</p>
            </div>
          }
        </div>
      </div>
    </div>
  `,
  styles: `
    :host {
      display: block;
      height: 100vh;
    }
    z-tab-group {
      display: flex;
      flex-direction: column;
      flex: 1;
      overflow: hidden;
    }
    .tab-content {
      flex: 1;
      overflow: hidden;
    }
    [role="tabpanel"] {
      height: 100%;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EditorComponent implements OnInit {
  private readonly authService = inject(AuthService);
  private readonly cronService = inject(CronService);
  protected readonly store = inject(FunctionsStore);

  protected readonly localName = signal('');
  protected readonly localScript = signal('');
  protected readonly localPackageJson = signal('');
  protected readonly localPlainEnv = signal('');
  protected readonly showRunner = signal(false);
  protected readonly showLogs = signal(false);
  protected readonly cronFunctions = signal<Set<string>>(new Set());

  private syncingFromStore = false;

  protected readonly isDirty = computed(() => {
    const fn = this.store.selectedFunction();
    if (!fn) return false;
    return (
      this.localName() !== fn.name ||
      this.localScript() !== fn.script ||
      this.localPackageJson() !== fn.packageJson ||
      this.localPlainEnv() !== ''
    );
  });

  constructor() {
    effect(() => {
      const fn = this.store.selectedFunction();
      this.syncingFromStore = true;
      this.localName.set(fn?.name ?? '');
      this.localScript.set(fn?.script ?? '');
      this.localPackageJson.set(fn?.packageJson ?? '');
      this.localPlainEnv.set('');
      this.syncingFromStore = false;
    });
  }

  ngOnInit(): void {
    this.store.loadFunctions();
    this.loadCronFunctions();
  }

  protected async loadCronFunctions(): Promise<void> {
    const res = await firstValueFrom(this.cronService.listAll());
    const names = new Set<string>();
    for (const cron of res.items) {
      if (cron.active) {
        names.add(cron.functionName);
      }
    }
    this.cronFunctions.set(names);
  }

  @HostListener('window:keydown', ['$event'])
  onKeydown(event: KeyboardEvent): void {
    if ((event.ctrlKey || event.metaKey) && event.key === 's') {
      event.preventDefault();
      if (this.isDirty()) {
        this.save();
      }
    }
  }

  protected async save(): Promise<void> {
    const fn = this.store.selectedFunction();
    if (!fn) return;

    const data: Record<string, unknown> = {
      name: this.localName(),
      script: this.localScript(),
      packageJson: this.localPackageJson(),
    };

    const envText = this.localPlainEnv().trim();
    if (envText) {
      try {
        data['plainEnv'] = JSON.parse(envText);
      } catch {
        alert('Invalid JSON in environment variables.');
        return;
      }
    }

    await this.store.updateFunction(fn.id, data as any);
  }

  protected onSelectFunction(id: string): void {
    if (this.isDirty() && !confirm('You have unsaved changes. Discard and switch?')) {
      return;
    }
    this.store.selectFunction(id);
  }

  protected async onCreateFunction(): Promise<void> {
    const name = prompt('Function name:');
    if (!name?.trim()) return;

    try {
      await this.store.createFunction(name.trim());
    } catch (e) {
      alert(`Failed to create function: ${(e as Error).message}`);
    }
  }

  protected async onDeleteFunction(id: string): Promise<void> {
    const fn = this.store.sortedFunctions().find((f) => f.id === id);
    if (!fn) return;
    if (!confirm(`Delete function "${fn.name}"?`)) return;

    try {
      await this.store.deleteFunction(id);
    } catch (e) {
      alert(`Failed to delete function: ${(e as Error).message}`);
    }
  }

  protected logout(): void {
    this.authService.logout();
  }
}
