import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
} from '@angular/core';
import type { HttpErrorResponse } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

import { FilesService } from '@/editor/files.service';
import type {
  FunctionFileEntry,
  FunctionFileRefusal,
} from '@/models/function-file.model';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * Shows what a function's directory holds on disk — the files the editor writes
 * there, `bun.lock`, `node_modules`, and anything a run left behind. Read-only:
 * no rename, no delete, no upload. Editing the managed files stays where it is,
 * in the Script and package.json tabs.
 *
 * `node_modules` gets no special rendering. It is a directory row like any
 * other, carrying the entry count every directory row carries, and one click
 * walks into it.
 */
@Component({
  selector: 'app-files-tab',
  standalone: true,
  imports: [ZardButtonComponent, ZardIconComponent],
  template: `
    <div class="flex h-full flex-col">
      <!-- Where we are, and the one way back up. -->
      <div class="flex items-center gap-2 border-b border-border px-3 py-1.5">
        <button
          z-button
          zType="ghost"
          zSize="sm"
          [disabled]="isRoot()"
          (click)="goUp()"
          title="Parent directory"
        >
          <z-icon zType="arrow-up" class="h-3.5 w-3.5" />
        </button>
        <span class="flex-1 truncate font-mono text-xs text-muted-foreground">
          {{ functionName() }}/{{ path() }}
        </span>
        <button z-button zType="ghost" zSize="sm" (click)="reload()" title="Refresh">
          <z-icon zType="refresh-cw" class="h-3.5 w-3.5" />
        </button>
      </div>

      <div class="flex min-h-0 flex-1">
        <!-- Level listing: directories first, then files, each group by name. -->
        <div class="w-1/2 overflow-y-auto border-r border-border">
          @if (listError()) {
            <p class="p-3 text-xs text-destructive">{{ listError() }}</p>
          } @else if (loading()) {
            <div class="flex items-center justify-center py-4">
              <z-icon zType="loader-circle" class="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          } @else {
            @for (entry of entries(); track entry.name) {
              <button
                class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors hover:bg-accent/30"
                [class.bg-accent]="!entry.dir && selected() === join(entry.name)"
                (click)="onEntryClick(entry)"
              >
                <z-icon
                  [zType]="entry.dir ? 'folder' : 'file'"
                  class="h-3.5 w-3.5 shrink-0 text-muted-foreground"
                />
                <span class="flex-1 truncate font-mono">{{ entry.name }}</span>
                <span class="shrink-0 text-muted-foreground">
                  {{ entry.dir ? entryCount(entry) : formatSize(entry.size) }}
                </span>
                <span class="shrink-0 text-muted-foreground">{{ formatDate(entry.modified) }}</span>
              </button>
            } @empty {
              <p class="p-3 text-xs text-muted-foreground">Nothing here yet.</p>
            }
          }
        </div>

        <!-- Reading pane. -->
        <div class="flex w-1/2 flex-col overflow-hidden">
          @if (!selected()) {
            <p class="p-3 text-xs text-muted-foreground">Select a file to read it.</p>
          } @else {
            <div class="flex items-center gap-2 border-b border-border px-3 py-1.5">
              <span class="flex-1 truncate font-mono text-xs">{{ selected() }}</span>
              <button z-button zType="outline" zSize="sm" (click)="download()">Download</button>
            </div>
            @if (fileLoading()) {
              <div class="flex items-center justify-center py-4">
                <z-icon zType="loader-circle" class="h-4 w-4 animate-spin text-muted-foreground" />
              </div>
            } @else if (notice()) {
              <p class="p-3 text-xs text-muted-foreground">{{ notice() }}</p>
            } @else {
              <pre
                class="min-h-0 flex-1 overflow-auto p-3 font-mono text-xs"
                >{{ content() }}</pre
              >
            }
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
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FilesTabComponent {
  private readonly filesService = inject(FilesService);

  readonly functionName = input.required<string>();

  /** Current directory, relative to the function root. Empty means the root. */
  protected readonly path = signal('');
  protected readonly entries = signal<FunctionFileEntry[]>([]);
  protected readonly loading = signal(false);
  protected readonly listError = signal('');

  /** Path of the file being read, relative to the function root. */
  protected readonly selected = signal('');
  protected readonly content = signal('');
  protected readonly fileLoading = signal(false);
  /** Why the file is not on screen: binary, too large, or unreadable. */
  protected readonly notice = signal('');

  protected readonly isRoot = computed(() => this.path() === '');

  constructor() {
    // Only depends on the function: navigating writes the signals below and
    // loads on its own, so re-running here would fight the navigation.
    effect(() => {
      const name = this.functionName();
      this.path.set('');
      this.clearPane();
      this.loadLevel(name, '');
    });
  }

  protected onEntryClick(entry: FunctionFileEntry): void {
    if (entry.dir) {
      this.enter(this.join(entry.name));
    } else {
      this.openFile(this.join(entry.name));
    }
  }

  protected goUp(): void {
    const parts = this.path().split('/');
    parts.pop();
    this.enter(parts.join('/'));
  }

  protected reload(): void {
    this.loadLevel(this.functionName(), this.path());
  }

  /** Joins a name onto the current directory. Public to the template only. */
  protected join(name: string): string {
    return this.path() ? `${this.path()}/${name}` : name;
  }

  private enter(path: string): void {
    this.path.set(path);
    this.clearPane();
    this.loadLevel(this.functionName(), path);
  }

  private clearPane(): void {
    this.selected.set('');
    this.content.set('');
    this.notice.set('');
  }

  private async loadLevel(name: string, path: string): Promise<void> {
    this.loading.set(true);
    this.listError.set('');
    try {
      const listing = await firstValueFrom(this.filesService.list(name, path));
      // A slow answer must not land on the function the user switched to.
      if (this.functionName() !== name || this.path() !== path) return;
      this.entries.set(listing.entries);
    } catch (e) {
      if (this.functionName() !== name || this.path() !== path) return;
      this.entries.set([]);
      this.listError.set(readServerMessage(e, 'Could not read this directory.'));
    } finally {
      this.loading.set(false);
    }
  }

  private async openFile(path: string): Promise<void> {
    this.selected.set(path);
    this.content.set('');
    this.notice.set('');
    this.fileLoading.set(true);
    try {
      const file = await firstValueFrom(this.filesService.read(this.functionName(), path));
      if (this.selected() !== path) return;
      this.content.set(file.content);
    } catch (e) {
      if (this.selected() !== path) return;
      this.notice.set(refusalMessage(e));
    } finally {
      this.fileLoading.set(false);
    }
  }

  protected async download(): Promise<void> {
    const path = this.selected();
    if (!path) return;
    try {
      const blob = await firstValueFrom(this.filesService.download(this.functionName(), path));
      // The route is superuser-only, so the file is fetched with the session
      // token and handed to the browser from memory; a plain link would go out
      // unauthenticated and come back a 401.
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = path.split('/').pop() ?? path;
      anchor.click();
      // Revoking in the same tick can cancel the save in some browsers.
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (e) {
      this.notice.set(readServerMessage(e, 'Download failed.'));
    }
  }

  protected entryCount(entry: FunctionFileEntry): string {
    if (entry.entries === undefined) return '';
    return entry.entries === 1 ? '1 entry' : `${entry.entries} entries`;
  }

  protected formatSize(size?: number): string {
    if (size === undefined) return '';
    if (size < 1024) return `${size} B`;
    if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`;
    return `${(size / (1024 * 1024)).toFixed(1)} MB`;
  }

  protected formatDate(modified: string): string {
    const date = new Date(modified);
    if (isNaN(date.getTime())) return '';
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
  }
}

/**
 * What the server refused to render, in its own words. The reason lives in the
 * response body — the status text only repeats the code.
 */
function refusalMessage(e: unknown): string {
  const body = (e as HttpErrorResponse)?.error as FunctionFileRefusal | null;
  if (body?.error === 'binary') {
    return 'This file is binary. Download it to inspect it.';
  }
  if (body?.error === 'too large') {
    const size = body.size === undefined ? '' : ` (${body.size} bytes)`;
    return `This file is too large to display${size}. Download it instead.`;
  }
  return readServerMessage(e, 'Could not read this file.');
}

function readServerMessage(e: unknown, fallback: string): string {
  const body = (e as HttpErrorResponse)?.error as { error?: string } | null;
  return typeof body?.error === 'string' ? body.error : fallback;
}
