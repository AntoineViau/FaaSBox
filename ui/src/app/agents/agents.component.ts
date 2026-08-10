import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { AuthService } from '@/auth/auth.service';
import { ThemeToggleComponent } from '@/theme/theme-toggle.component';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * How to plug an AI agent into this instance.
 *
 * The page composes nothing: it shows the integration snippet of each client
 * with the address of the instance already in it, and the user copies. Written
 * by hand, that address is the one thing everyone gets wrong — and an example
 * carved on localhost works nowhere else.
 *
 * The key is a placeholder, for the reason the invoke banner gives: a key's
 * value is only ever shown at creation, so the editor has nothing real to put
 * there.
 */
type AgentClient = {
  readonly id: string;
  /** What the user calls it. */
  readonly name: string;
  /** Where the snippet goes: a terminal, or a file that has to be named. */
  readonly target: string;
  readonly snippet: string;
};

/** The placeholder every snippet carries, spelled once. */
const KEY_PLACEHOLDER = 'fbx_your_key_here';

@Component({
  selector: 'app-agents',
  standalone: true,
  imports: [RouterLink, ThemeToggleComponent, ZardButtonComponent, ZardIconComponent],
  template: `
    <div class="mx-auto flex h-screen w-full max-w-app flex-col">
      <header class="flex items-center justify-between border-b border-border px-4 py-2">
        <div class="flex items-center gap-3">
          <a z-button zType="ghost" zSize="sm" routerLink="/editor">
            <z-icon zType="arrow-left" class="mr-1.5 h-4 w-4" />
            Functions
          </a>
          <h1 class="text-lg font-semibold">AI agents</h1>
        </div>
        <div class="flex items-center gap-2">
          <app-theme-toggle />
          <button z-button zType="outline" zSize="sm" (click)="logout()">
            <z-icon zType="log-out" class="mr-1.5 h-4 w-4" />
            Sign out
          </button>
        </div>
      </header>

      <div class="flex-1 overflow-y-auto p-4">
        <div class="mb-4 space-y-2 text-sm text-muted-foreground">
          <p>
            This instance speaks
            <a
              href="https://modelcontextprotocol.io"
              target="_blank"
              rel="noreferrer"
              class="underline underline-offset-2 hover:text-foreground"
              >MCP</a
            >
            at <code class="font-mono text-foreground">{{ endpoint() }}</code
            >. An agent connected to it lists, reads, writes, runs and inspects the functions of this
            instance, and is told how to write one when it connects.
          </p>
          <p>
            It needs an API key carrying <strong class="text-foreground">Can manage functions</strong>, and
            that key must have an <strong class="text-foreground">open scope</strong>: creating a function
            is refused to a restricted one. An agent that can create can therefore reach every function
            here. Create one on the
            <a routerLink="/keys" class="underline underline-offset-2 hover:text-foreground">API keys</a>
            page, then replace
            <code class="font-mono text-foreground">{{ placeholder }}</code> below.
          </p>
        </div>

        @for (client of clients(); track client.id) {
          <div class="mb-3 rounded-lg border border-border p-3">
            <div class="flex items-baseline justify-between gap-3">
              <div class="flex items-baseline gap-2">
                <span class="text-sm font-semibold">{{ client.name }}</span>
                <span class="text-xs text-muted-foreground">{{ client.target }}</span>
              </div>
              <button z-button zType="ghost" zSize="sm" (click)="copy(client)">
                <z-icon [zType]="copied() === client.id ? 'check' : 'copy'" class="mr-1.5 h-3.5 w-3.5" />
                {{ copied() === client.id ? 'Copied' : 'Copy' }}
              </button>
            </div>
            <pre
              class="mt-2 overflow-x-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs"
              >{{ client.snippet }}</pre
            >
          </div>
        }
      </div>
    </div>
  `,
  styles: `
    :host {
      display: block;
      height: 100vh;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgentsComponent {
  private readonly authService = inject(AuthService);

  protected readonly placeholder = KEY_PLACEHOLDER;

  /**
   * The address is read from the browser, like the invoke banner does and for
   * the same reason: the snippet has to work as it stands on the instance it is
   * displayed from.
   */
  protected readonly endpoint = computed(() => `${window.location.origin}/mcp`);

  /** Which card said "Copied" last, so the confirmation is per card. */
  protected readonly copied = signal('');

  protected readonly clients = computed<AgentClient[]>(() => {
    const url = this.endpoint();
    return [
      {
        id: 'claude-code',
        name: 'Claude Code',
        target: 'run it in a terminal',
        snippet: `claude mcp add --transport http faasbox ${url} \\\n  --header "X-API-Key: ${KEY_PLACEHOLDER}"`,
      },
      {
        id: 'codex',
        name: 'Codex',
        target: '~/.codex/config.toml',
        snippet: `[mcp_servers.faasbox]\nurl = "${url}"\nhttp_headers = { "X-API-Key" = "${KEY_PLACEHOLDER}" }`,
      },
      {
        id: 'opencode',
        name: 'OpenCode',
        target: 'opencode.json',
        snippet: JSON.stringify(
          {
            mcp: {
              faasbox: {
                type: 'remote',
                url,
                enabled: true,
                headers: { 'X-API-Key': KEY_PLACEHOLDER },
              },
            },
          },
          null,
          2,
        ),
      },
      {
        id: 'generic',
        name: 'Any other client',
        target: 'the mcpServers block most of them read',
        snippet: JSON.stringify(
          {
            mcpServers: {
              faasbox: {
                type: 'http',
                url,
                headers: { 'X-API-Key': KEY_PLACEHOLDER },
              },
            },
          },
          null,
          2,
        ),
      },
    ];
  });

  protected async copy(client: AgentClient): Promise<void> {
    try {
      await navigator.clipboard.writeText(client.snippet);
      this.copied.set(client.id);
    } catch {
      // Refused permission, or an insecure origin. The snippet is on screen and
      // selectable either way, so there is nothing to report and nothing to fix.
      this.copied.set('');
    }
  }

  protected logout(): void {
    this.authService.logout();
  }
}
