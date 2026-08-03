import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  HostListener,
  inject,
  type OnInit,
  signal,
  viewChild,
} from '@angular/core';
import { firstValueFrom } from 'rxjs';

import { AuthService } from '@/auth/auth.service';
import { CronService } from '@/editor/cron.service';
import { FunctionsStore } from '@/editor/functions.store';
import { CodeEditorComponent } from '@/editor/code-editor.component';
import { CronEditorComponent } from '@/editor/cron-editor.component';
import { EnvEditorComponent } from '@/editor/env-editor.component';
import { LogViewerComponent } from '@/editor/log-viewer.component';
import { RunnerComponent } from '@/editor/runner.component';
import { SidebarComponent } from '@/editor/sidebar.component';
import { ThemeToggleComponent } from '@/theme/theme-toggle.component';
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
    EnvEditorComponent,
    LogViewerComponent,
    RunnerComponent,
    SidebarComponent,
    ThemeToggleComponent,
  ],
  template: `
    <div class="mx-auto flex h-screen w-full max-w-app flex-col">
      <!-- Header. The title block matches the sidebar width, so the actions
           below start exactly where the editor area does. -->
      <header class="flex items-center border-b border-border py-2">
        <div class="w-56 shrink-0 px-4">
          <h1 class="text-lg font-semibold">FaaSBox</h1>
        </div>
        <div class="flex flex-1 items-center gap-2 px-4">
          <!-- Panels of the open function, so they only exist when there is one.
               Save is not here: it lives next to the name it records. -->
          @if (store.selectedFunction()) {
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
          }
          <div class="ml-auto flex items-center gap-2">
            <app-theme-toggle />
            <button z-button zType="outline" zSize="sm" (click)="logout()">
              <z-icon zType="log-out" class="mr-1.5 h-4 w-4" />
              Sign out
            </button>
          </div>
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
            <!-- Name field, and the one Save of the function: it records the
                 name, the script and the package.json, from any of the tabs. -->
            <div class="flex items-center gap-2 border-b border-border px-4 py-2">
              <input
                z-input
                type="text"
                placeholder="Function name"
                [value]="localName()"
                (input)="localName.set($any($event.target).value)"
                class="h-8 flex-1 text-sm"
              />
              @if (nameOrScriptDirty()) {
                <button z-button zType="default" zSize="sm" (click)="save()">
                  <z-icon zType="save" class="mr-1.5 h-4 w-4" />
                  Save
                </button>
              }
            </div>

            <!-- Unsaved package.json banner: outside the tab panels on purpose,
                 so it stays visible after leaving the package.json tab. -->
            @if (packageJsonDirty()) {
              <div
                class="flex items-center gap-2 border-b border-yellow-500/30 bg-yellow-500/10 px-4 py-2 text-xs text-yellow-600 dark:text-yellow-500"
              >
                <z-icon zType="triangle-alert" class="h-4 w-4 shrink-0" />
                <span class="flex-1">
                  package.json has unsaved changes. Dependencies are only installed when you save, and
                  runs execute the last saved version.
                </span>
                <button z-button zType="default" zSize="sm" (click)="save()">
                  <z-icon zType="save" class="mr-1.5 h-4 w-4" />
                  Save
                </button>
              </div>
            }

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
                <app-env-editor [functionId]="fn.id" [functionName]="fn.name" />
              </z-tab>
            </z-tab-group>

            <!-- Runner panel -->
            @if (showRunner()) {
              <div class="h-64 shrink-0 border-t border-border">
                <app-runner
                  [functionName]="fn.name"
                  [busy]="running()"
                  [dirty]="isDirty()"
                  (run)="run()"
                  (saveAndRun)="saveAndRun()"
                />
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
  protected readonly showRunner = signal(true);
  protected readonly showLogs = signal(true);
  protected readonly running = signal(false);
  protected readonly cronFunctions = signal<Set<string>>(new Set());

  private readonly runner = viewChild(RunnerComponent);
  private readonly envEditor = viewChild(EnvEditorComponent);

  /** Guards the sync effect below: see why it keys on the id and not the record. */
  private lastSyncedId: string | null = null;

  /**
   * Isolated from isDirty because this one has no visible effect: dependencies
   * are only installed when the function is saved, so an edited package.json
   * that was never saved installs nothing at all.
   */
  protected readonly packageJsonDirty = computed(() => {
    const fn = this.store.selectedFunction();
    return !!fn && this.localPackageJson() !== fn.packageJson;
  });

  /**
   * What the Save next to the name field answers for. It writes all three
   * fields, but it only shows up for these two: a package.json edited alone
   * already has the banner and its button, and a second call to action on the
   * same record would just ask twice for the same click.
   */
  protected readonly nameOrScriptDirty = computed(() => {
    const fn = this.store.selectedFunction();
    return !!fn && (this.localName() !== fn.name || this.localScript() !== fn.script);
  });

  /** Environment lives in its own tab, with its own Save: it is not part of this. */
  protected readonly isDirty = computed(() => this.nameOrScriptDirty() || this.packageJsonDirty());

  constructor() {
    effect(() => {
      const fn = this.store.selectedFunction();
      // Only on a change of selection. The store record is also patched by every
      // save - including the Environment tab's own - and re-filling the buffers
      // then would discard whatever was typed during the round trip.
      if (fn?.id === this.lastSyncedId) return;
      this.lastSyncedId = fn?.id ?? null;
      this.localName.set(fn?.name ?? '');
      this.localScript.set(fn?.script ?? '');
      this.localPackageJson.set(fn?.packageJson ?? '');
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

  /** Returns false when nothing was written, so callers can stop there. */
  protected async save(): Promise<boolean> {
    const fn = this.store.selectedFunction();
    if (!fn) return false;

    const data: Record<string, unknown> = {
      name: this.localName(),
      script: this.localScript(),
      packageJson: this.localPackageJson(),
    };

    try {
      // The store holds the saved record, so the unsaved markers clear as soon
      // as it is patched - including the package.json banner. plainEnv is left
      // out entirely: omitting it is what tells the server to keep the secrets
      // it holds, and the Environment tab writes them on its own.
      await this.store.updateFunction(fn.id, data as any);
      return true;
    } catch (e) {
      alert(`Failed to save: ${(e as Error).message}`);
      return false;
    }
  }

  /**
   * Runs what the server already holds. /invoke executes the file on disk, so
   * this is the last saved version — which is the whole point when the buffer
   * has not been touched. The runner only offers Save and run past that.
   */
  protected async run(): Promise<void> {
    if (this.running()) return;
    this.running.set(true);
    try {
      await this.runner()?.execute();
    } finally {
      this.running.set(false);
    }
  }

  /**
   * The runner posts to /invoke, which executes the file on disk: saving first
   * is what makes it diagnose the code on screen. A changed package.json is
   * saved along with the script, and the invocation waits for the install the
   * save triggers rather than running against stale dependencies.
   */
  protected async saveAndRun(): Promise<void> {
    if (this.running()) return;
    this.running.set(true);
    try {
      if (await this.save()) {
        await this.runner()?.execute();
      }
    } finally {
      this.running.set(false);
    }
  }

  protected onSelectFunction(id: string): void {
    // The Environment tab saves itself, so it is absent from isDirty - but
    // leaving it behind unsaved loses just as much.
    const dirty = this.isDirty() || (this.envEditor()?.isDirty() ?? false);
    if (dirty && !confirm('You have unsaved changes. Discard and switch?')) {
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
