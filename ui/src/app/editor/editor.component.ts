/**
 * Orchestrator of the editor: the open function, the local buffers, and the
 * five tabs. The URL is the source of truth for what is open — applyRoute below
 * is the only writer of the selection and of the active tab.
 *
 * This file is knowingly past the 300-line readability soft limit, and two
 * things put it there. The tab bar is written here rather than borrowed: the
 * vendored tab group kept its active index to itself, so the URL could neither
 * drive it nor read it. And the five panels belong to the orchestrator, since
 * they must all be rendered at once and merely hidden — destroying them would
 * drop unsaved input and break the isDirty() the switch confirmation
 * aggregates. The lever, the day it is worth pulling, is to extract the <nav>
 * into a presentational tab-bar component; the panels stay here.
 */
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  effect,
  type ElementRef,
  HostListener,
  inject,
  type OnInit,
  signal,
  untracked,
  viewChild,
  viewChildren,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute, Router } from '@angular/router';
import { firstValueFrom } from 'rxjs';

import { AuthService } from '@/auth/auth.service';
import { TriggersService } from '@/editor/triggers.service';
import { FunctionsStore } from '@/editor/functions.store';
import { CodeEditorComponent } from '@/editor/code-editor.component';
import { TriggersEditorComponent } from '@/editor/triggers-editor.component';
import { DepsStatusComponent } from '@/editor/deps-status.component';
import { type HeaderRow, readSampleHeaders, serializeHeaders } from '@/editor/request-headers';
import {
  DEFAULT_EDITOR_TAB,
  EDITOR_TABS,
  EDITOR_TAB_SLUGS,
  type EditorTabSlug,
  isEditorTabSlug,
} from '@/editor/editor-tabs';
import { EnvEditorComponent } from '@/editor/env-editor.component';
import { errorText } from '@/editor/error-text';
import { FilesTabComponent } from '@/editor/files-tab.component';
import { InvokeHintComponent } from '@/editor/invoke-hint.component';
import { DEMO_MODE_HINT, InstanceService } from '@/instance/instance.service';
import { LogViewerComponent } from '@/editor/log-viewer.component';
import { RunnerComponent } from '@/editor/runner.component';
import { SidebarComponent } from '@/editor/sidebar.component';
import { ThemeToggleComponent } from '@/theme/theme-toggle.component';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';
import { ZardInputDirective } from '@shared/components/input';

@Component({
  selector: 'app-editor',
  standalone: true,
  imports: [
    ZardButtonComponent,
    ZardIconComponent,
    ZardInputDirective,
    CodeEditorComponent,
    TriggersEditorComponent,
    DepsStatusComponent,
    EnvEditorComponent,
    FilesTabComponent,
    InvokeHintComponent,
    LogViewerComponent,
    RunnerComponent,
    SidebarComponent,
    ThemeToggleComponent,
  ],
  template: `
    <div class="mx-auto flex h-full w-full max-w-app flex-col">
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
            [triggerFunctions]="triggerFunctions()"
            [loading]="store.isLoading()"
            [demoMode]="demoMode()"
            (select)="onSelectFunction($event)"
            (create)="onCreateFunction()"
            (deleteItem)="onDeleteFunction($event)"
            (refresh)="onRefresh()"
          />
        </div>

        <!-- Editor area -->
        <div class="flex flex-1 flex-col overflow-hidden">
          @if (store.selectedFunction(); as fn) {
            <!-- Name field, and the one Save of the function: it records the
                 name, the script and the package.json, from any of the tabs. -->
            <div class="flex items-center gap-2 border-b border-border px-4 py-2">
              <!-- The hint goes on the wrapper of every control the demo mode
                   closes: a disabled element receives no hover event, so a
                   title on it would never show. -->
              <span class="flex-1" [title]="demoMode() ? DEMO_MODE_HINT : ''">
                <input
                  z-input
                  type="text"
                  placeholder="Function name"
                  [value]="localName()"
                  [disabled]="demoMode()"
                  (input)="localName.set($any($event.target).value)"
                  class="h-8 w-full text-sm"
                />
              </span>
              <!-- Shown while there is something to save, and on a showcase,
                   where nothing can become dirty: the button is part of what
                   the instance is there to display. -->
              @if (nameScriptOrSampleDirty() || demoMode()) {
                <span [title]="demoMode() ? DEMO_MODE_HINT : ''">
                  <button
                    z-button
                    zType="default"
                    zSize="sm"
                    [zDisabled]="demoMode()"
                    (click)="save()"
                  >
                    <z-icon zType="save" class="mr-1.5 h-4 w-4" />
                    Save
                  </button>
                </span>
              }
            </div>

            <!-- How to call this function from outside. It names the saved
                 name, which is the one /invoke answers to right now. -->
            <app-invoke-hint [name]="fn.name" />

            <!-- Renaming is allowed and this does not stand in its way: it says
                 what it costs, and clears itself as soon as the field is back
                 on the saved name. A creation never sees it — the record
                 already exists by the time the editor opens on it. -->
            @if (nameDirty()) {
              <div
                class="flex items-center gap-2 border-b border-yellow-500/30 bg-yellow-500/10 px-4 py-2 text-xs text-yellow-600 dark:text-yellow-500"
              >
                <z-icon zType="triangle-alert" class="h-4 w-4 shrink-0" />
                <span class="flex-1">
                  Renaming changes the invocation URL. Callers that use this function's name will
                  break — those that use its id keep working.
                </span>
              </div>
            }

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

            <!-- Tabs, written here rather than taken from shared/: the vendored
                 tab group keeps its active index to itself, so the URL could
                 neither drive it nor read it. -->
            <div class="flex flex-1 flex-col overflow-hidden">
              <nav
                role="tablist"
                aria-label="Function"
                class="tab-bar mb-4 flex flex-row justify-start gap-4 overflow-auto border-b"
                (keydown)="onTabKeydown($event)"
              >
                @for (t of TABS; track t.slug) {
                  <button
                    #tabButton
                    type="button"
                    role="tab"
                    [id]="'tab-' + t.slug"
                    [attr.aria-controls]="'panel-' + t.slug"
                    [attr.aria-selected]="activeTab() === t.slug"
                    [attr.tabindex]="activeTab() === t.slug ? 0 : -1"
                    class="inline-flex h-8 shrink-0 cursor-pointer select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-none border border-transparent bg-clip-padding px-2.5 text-sm font-medium outline-none transition-all hover:text-foreground dark:hover:bg-muted/50 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
                    [class.border-b-2]="activeTab() === t.slug"
                    [class.border-b-primary]="activeTab() === t.slug"
                    (click)="goToTab(t.slug)"
                  >
                    {{ t.label }}
                  </button>
                }
              </nav>

              <!-- Every panel is rendered and merely hidden. Destroying them
                   would drop what was typed in the Environment tab and break
                   the isDirty() the switch confirmation aggregates by
                   viewChild. The switch below is keyed on a constant, so each
                   panel renders its own branch once and keeps it. -->
              <div class="tab-content flex-1">
                @for (t of TABS; track t.slug) {
                  <div
                    role="tabpanel"
                    [id]="'panel-' + t.slug"
                    [attr.aria-labelledby]="'tab-' + t.slug"
                    tabindex="0"
                    [hidden]="activeTab() !== t.slug"
                    class="outline-none focus-visible:ring-2 focus-visible:ring-primary/50"
                  >
                    @switch (t.slug) {
                      @case ('script') {
                        <div class="h-full">
                          <app-code-editor
                            [content]="localScript()"
                            language="typescript"
                            [readOnly]="demoMode()"
                            (contentChange)="localScript.set($event)"
                          />
                        </div>
                      }
                      @case ('package-json') {
                        <div class="flex h-full flex-col">
                          <!-- Install state, pushed by the server as it changes. -->
                          <app-deps-status [status]="fn.depsStatus" [error]="fn.depsError" />
                          <div class="min-h-0 flex-1">
                            <app-code-editor
                              [content]="localPackageJson()"
                              language="json"
                              [readOnly]="demoMode()"
                              (contentChange)="localPackageJson.set($event)"
                            />
                          </div>
                        </div>
                      }
                      @case ('triggers') {
                        <app-triggers-editor
                          [functionId]="fn.id"
                          [functionName]="fn.name"
                          [demoMode]="demoMode()"
                          (triggerCountChange)="loadTriggerFunctions()"
                        />
                      }
                      @case ('environment') {
                        <app-env-editor [functionId]="fn.id" [demoMode]="demoMode()" />
                      }
                      @case ('files') {
                        <app-files-tab [functionId]="fn.id" [functionName]="fn.name" />
                      }
                    }
                  </div>
                }
              </div>
            </div>

            <!-- Runner panel. Taller than the logs below it: it stacks the
                 request over the result, and 16rem would leave each of the two
                 rows a couple of lines. -->
            @if (showRunner()) {
              <div class="h-96 shrink-0 border-t border-border">
                <app-runner
                  [functionId]="fn.id"
                  [functionName]="fn.name"
                  [(body)]="localSampleBody"
                  [(headers)]="localSampleHeaders"
                  [busy]="running()"
                  [dirty]="isDirty()"
                  [demoMode]="demoMode()"
                  (run)="run()"
                  (saveAndRun)="saveAndRun()"
                />
              </div>
            }

            <!-- Logs panel -->
            @if (showLogs()) {
              <div class="h-64 shrink-0 border-t border-border">
                <app-log-viewer [functionId]="fn.id" />
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
      height: 100%;
    }
    /* These three hold the tab group up, and they live here now that the markup
       is the editor's own — they had to be global while it was built by the
       vendored tab group and carried its scope attribute. Without them the
       panel is sized by its content: a long script grows the column, and the
       tab bar, a scroll container so shrinkable to nothing, simply vanishes. */
    .tab-content {
      min-height: 0;
    }
    [role='tabpanel'] {
      height: 100%;
      overflow: auto;
    }
    .tab-bar {
      flex-shrink: 0;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EditorComponent implements OnInit {
  private readonly authService = inject(AuthService);
  private readonly triggersService = inject(TriggersService);
  private readonly destroyRef = inject(DestroyRef);
  private readonly instance = inject(InstanceService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  protected readonly store = inject(FunctionsStore);

  protected readonly TABS = EDITOR_TABS;

  /**
   * A showcase: everything is on screen, nothing can be written. The page
   * reads it from the service and hands it down — the panels below stay
   * presentational.
   */
  protected readonly demoMode = this.instance.demoMode;
  protected readonly DEMO_MODE_HINT = DEMO_MODE_HINT;

  /**
   * The address bar is the source of truth: these two are the only inputs of
   * the selection and of the active tab, and applyRoute is the only writer of
   * either. One direction, so no state here can drift away from the URL.
   */
  private readonly params = toSignal(this.route.paramMap, { requireSync: true });
  private readonly routeId = computed(() => this.params().get('id'));
  private readonly routeTab = computed(() => this.params().get('tab'));

  protected readonly activeTab = signal<EditorTabSlug>(DEFAULT_EDITOR_TAB);

  /**
   * What applyRoute last acted on: what a change of id is measured against, and
   * what tells a refused switch from an accepted one. A signal rather than a
   * field because the unknown-id effect below reads it - and would otherwise
   * depend on the order the two effects happen to run in.
   */
  private readonly appliedId = signal<string | null>(null);

  /**
   * Until the list is in, an id that matches nothing means nothing: on a cold
   * load the route lands long before the functions do.
   */
  private readonly listLoaded = signal(false);

  protected readonly localName = signal('');
  protected readonly localScript = signal('');
  protected readonly localPackageJson = signal('');
  /**
   * The sample call, held here like the script and for the same reason: it is a
   * field of the function, and switching function is a load. The Runner edits
   * it through a two-way binding and owns none of it — which is what makes the
   * panel follow the selection instead of trailing a function behind.
   */
  protected readonly localSampleBody = signal('');
  protected readonly localSampleHeaders = signal<HeaderRow[]>([]);
  protected readonly showRunner = signal(true);
  protected readonly showLogs = signal(true);
  protected readonly running = signal(false);
  /** Ids of the functions carrying at least one active trigger. */
  protected readonly triggerFunctions = signal<Set<string>>(new Set());

  private readonly runner = viewChild(RunnerComponent);
  private readonly envEditor = viewChild(EnvEditorComponent);
  // Both resolve from any tab: the tab bar hides panels, it does not destroy
  // them, so what was typed in one is still there while another is on screen.
  private readonly triggersEditor = viewChild(TriggersEditorComponent);
  private readonly tabButtons = viewChildren<ElementRef<HTMLButtonElement>>('tabButton');

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
   * The name field left the name the record carries. It is what the renaming
   * warning is pinned on, and it is only ever true for an existing function:
   * the editor has no unsaved-creation state, a function is written server-side
   * before it is opened, so its buffer starts on the saved name.
   */
  protected readonly nameDirty = computed(() => {
    const fn = this.store.selectedFunction();
    return !!fn && this.localName() !== fn.name;
  });

  /**
   * The sample call left what the record holds.
   *
   * A plain comparison, both sides read the same way, because the column says
   * exactly what the panel shows — no starting value is substituted for an
   * empty one. That is what makes an emptied body saveable: it differs from
   * what is stored, so Save appears, and once written it matches again.
   */
  protected readonly sampleDirty = computed(() => {
    const fn = this.store.selectedFunction();
    if (!fn) return false;
    return (
      this.localSampleBody() !== fn.sampleBody ||
      serializeHeaders(this.localSampleHeaders()) !==
        serializeHeaders(readSampleHeaders(fn.sampleHeaders))
    );
  });

  /**
   * What the Save next to the name field answers for. It writes every field it
   * can, but it only shows up for these three: a package.json edited alone
   * already has the banner and its button, and a second call to action on the
   * same record would just ask twice for the same click.
   *
   * The sample is in here rather than off on its own, and it is a trade taken
   * knowingly: typing a test body now makes `Save and run` appear where `Run`
   * used to be enough. That is honest — something is unsaved — and it is the
   * price of the sample surviving a change of function.
   */
  protected readonly nameScriptOrSampleDirty = computed(() => {
    const fn = this.store.selectedFunction();
    return this.nameDirty() || (!!fn && this.localScript() !== fn.script) || this.sampleDirty();
  });

  /** Environment lives in its own tab, with its own Save: it is not part of this. */
  protected readonly isDirty = computed(
    () => this.nameScriptOrSampleDirty() || this.packageJsonDirty(),
  );

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
      // Straight off the record, like everything above it: the starting sample
      // was written there when the function was created, so there is nothing to
      // substitute and an empty sample is an empty sample.
      this.localSampleBody.set(fn?.sampleBody ?? '');
      this.localSampleHeaders.set(readSampleHeaders(fn?.sampleHeaders ?? ''));
    });

    // URL -> state. Reading the parameters is all this effect tracks; what it
    // then decides depends on the buffers, and re-running on those would ask
    // for confirmation at every keystroke.
    effect(() => {
      const id = this.routeId();
      const tab = this.routeTab();
      untracked(() => this.applyRoute(id, tab));
    });

    // An id nothing answers to - a stale link, a function deleted here or
    // elsewhere - lands on the empty state rather than on a dead screen.
    //
    // Only once that id has actually been applied. A refused switch never
    // applies it, and bouncing it to /editor all the same would ask for
    // confirmation a second time on the way back.
    effect(() => {
      const id = this.routeId();
      const applied = this.appliedId();
      const loaded = this.listLoaded();
      const known = this.store.functions().some((f) => f.id === id);
      if (!id || applied !== id || !loaded || known) return;
      untracked(() => void this.router.navigate(['/editor'], { replaceUrl: true }));
    });
  }

  ngOnInit(): void {
    void this.store.loadFunctions().then(() => this.listLoaded.set(true));
    this.loadTriggerFunctions();

    // The install runs in the background for up to a minute after the save
    // answered, so the save response alone would show one frozen value. The
    // server pushes instead — nothing here polls.
    this.destroyRef.onDestroy(this.store.startDepsStateSync());
  }

  /** The one spelling of an editor address, so no caller builds it by hand. */
  private tabLink(id: string | null, tab: EditorTabSlug): unknown[] {
    return id ? ['/editor', id, 'tab', tab] : ['/editor'];
  }

  /**
   * Brings the selection and the active tab in line with the URL. It is the
   * only writer of both, and the only place the switch confirmation can live:
   * the browser Back button changes the function without going through any
   * click, and canDeactivate never fires on a parameter change.
   */
  private applyRoute(id: string | null, tab: string | null): void {
    if (id !== this.appliedId() && !this.mayLeave()) {
      // Refused: put the address bar back on the function being edited, in
      // place - a refusal has no business leaving a history entry behind.
      void this.router.navigate(this.tabLink(this.appliedId(), this.activeTab()), {
        replaceUrl: true,
      });
      return;
    }

    this.appliedId.set(id);
    this.store.selectFunction(id);

    if (!id) {
      this.activeTab.set(DEFAULT_EDITOR_TAB);
      return;
    }

    // A bare /editor/<id> and an unknown slug both land on the default tab,
    // and the address is corrected in place rather than stacked on.
    if (!isEditorTabSlug(tab)) {
      this.activeTab.set(DEFAULT_EDITOR_TAB);
      void this.router.navigate(this.tabLink(id, DEFAULT_EDITOR_TAB), { replaceUrl: true });
      return;
    }
    this.activeTab.set(tab);
  }

  /**
   * The Environment and Triggers tabs save themselves, so neither is part of
   * isDirty - but leaving either behind unsaved loses just as much. Triggers
   * especially: the panel discards its rows as soon as the function changes.
   *
   * A function that is already gone asks nothing: its deletion was confirmed on
   * its own, and there is nothing left to go back to.
   */
  private mayLeave(): boolean {
    const previous = this.appliedId();
    if (!previous || !this.store.functions().some((f) => f.id === previous)) return true;

    const dirty =
      this.isDirty() ||
      (this.envEditor()?.isDirty() ?? false) ||
      (this.triggersEditor()?.isDirty() ?? false);
    return !dirty || confirm('You have unsaved changes. Discard and switch?');
  }

  protected goToTab(slug: EditorTabSlug): void {
    const id = this.routeId();
    if (!id) return;
    void this.router.navigate(this.tabLink(id, slug));
  }

  /**
   * Arrows move the active tab, Home and End go to the ends. Without them the
   * roving tabindex would be a regression: the inactive tabs are out of the tab
   * order, so the keyboard would only ever reach the one already active.
   */
  protected onTabKeydown(event: KeyboardEvent): void {
    const count = EDITOR_TAB_SLUGS.length;
    const current = EDITOR_TAB_SLUGS.indexOf(this.activeTab());
    let next: number;
    switch (event.key) {
      case 'ArrowLeft':
        next = (current - 1 + count) % count;
        break;
      case 'ArrowRight':
        next = (current + 1) % count;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = count - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    this.goToTab(EDITOR_TAB_SLUGS[next]);
    this.tabButtons()[next]?.nativeElement.focus();
  }

  /**
   * Re-reads what the sidebar shows: the functions and their trigger markers.
   * The two go together — a function written from the API may arrive with its
   * triggers, and reloading one without the other would show it without its mark.
   *
   * The open buffers are left alone: the sync effect keys on the selected id,
   * which a reload does not change, so nothing typed here is discarded.
   */
  protected async onRefresh(): Promise<void> {
    await Promise.all([this.store.loadFunctions(), this.loadTriggerFunctions()]);
  }

  protected async loadTriggerFunctions(): Promise<void> {
    const res = await firstValueFrom(this.triggersService.listAll());
    const ids = new Set<string>();
    for (const trigger of res.items) {
      if (trigger.active) {
        ids.add(trigger.function);
      }
    }
    this.triggerFunctions.set(ids);
  }

  @HostListener('window:keydown', ['$event'])
  onKeydown(event: KeyboardEvent): void {
    if ((event.ctrlKey || event.metaKey) && event.key === 's') {
      event.preventDefault();
      if (this.isDirty() && !this.demoMode()) {
        this.save();
      }
    }
  }

  /** Returns false when nothing was written, so callers can stop there. */
  protected async save(): Promise<boolean> {
    if (this.demoMode()) return false;
    const fn = this.store.selectedFunction();
    if (!fn) return false;

    const data: Record<string, unknown> = {
      name: this.localName(),
      script: this.localScript(),
      packageJson: this.localPackageJson(),
      sampleBody: this.localSampleBody(),
      sampleHeaders: serializeHeaders(this.localSampleHeaders()),
    };

    try {
      // The store holds the saved record, so the unsaved markers clear as soon
      // as it is patched - including the package.json banner. plainEnv is left
      // out entirely: omitting it is what tells the server to keep the secrets
      // it holds, and the Environment tab writes them on its own.
      await this.store.updateFunction(fn.id, data as any);
      return true;
    } catch (e) {
      // The reason lives in the response body: a name outside the naming rule
      // is refused there, and the message names both the name and the rule.
      alert(`Failed to save: ${errorText(e)}`);
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

  /**
   * The sidebar navigates instead of selecting; the effect on the route
   * parameters does the rest, confirmation included.
   *
   * It lands on the Script tab rather than carrying the open one over. The five
   * tabs are not five views of one thing, they are five parts of a function,
   * and the one that says what a function *is* is its script: arriving on
   * another function's Files listing, or on triggers belonging to something one
   * has not read yet, is arriving nowhere.
   *
   * A saved link keeps naming its tab - applyRoute reads it off the URL, and
   * this rule lives at the click site precisely so that it does not reach
   * there.
   */
  protected onSelectFunction(id: string): void {
    void this.router.navigate(this.tabLink(id, DEFAULT_EDITOR_TAB));
  }

  protected async onCreateFunction(): Promise<void> {
    if (this.demoMode()) return;
    const name = prompt('Function name:');
    if (!name?.trim()) return;

    try {
      // The store does not select what it creates: opening it is a navigation
      // like any other, so the URL follows and stays the only source of truth —
      // on the script, like every other opening, which is also where the
      // example the function is created with can be read.
      const created = await this.store.createFunction(name.trim());
      void this.router.navigate(this.tabLink(created.id, DEFAULT_EDITOR_TAB));
    } catch (e) {
      alert(`Failed to create function: ${errorText(e)}`);
    }
  }

  protected async onDeleteFunction(id: string): Promise<void> {
    if (this.demoMode()) return;
    const fn = this.store.sortedFunctions().find((f) => f.id === id);
    if (!fn) return;
    if (!confirm(`Delete function "${fn.name}"?`)) return;

    try {
      await this.store.deleteFunction(id);
    } catch (e) {
      alert(`Failed to delete function: ${errorText(e)}`);
    }
  }

  protected logout(): void {
    this.authService.logout();
  }
}
