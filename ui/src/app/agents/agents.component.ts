import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  type OnInit,
  signal,
} from '@angular/core';
import { RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';

import { agentSnippets, KEY_PLACEHOLDER, llmsGuideUrl, llmsPrompt } from '@/agents/agent-snippets';
import { AgentsService } from '@/agents/agents.service';
import { AuthorizedAgentsComponent } from '@/agents/authorized-agents.component';
import { SnippetListComponent } from '@/agents/snippet-list.component';
import { AccountComponent } from '@/auth/account.component';
import { AuthService } from '@/auth/auth.service';
import { InstanceService } from '@/instance/instance.service';
import { type FaasboxOAuthGrant, grantClientName } from '@/models/faasbox-oauth-grant.model';
import { ThemeToggleComponent } from '@/theme/theme-toggle.component';
import { ZardAlertComponent } from '@shared/components/alert';
import { ZardButtonComponent } from '@shared/components/button';
import { ZardIconComponent } from '@shared/components/icon';

/**
 * How to plug an AI agent into this instance, and which ones are plugged in.
 *
 * **Three ways in, so three panels, all closed.** Two go through MCP and differ
 * by how the agent proves who it is; the third skips MCP altogether and hands
 * the agent a prompt that sends it to `/llms.txt`. Showing one set of snippets
 * and hiding the others behind a toggle asked the reader to choose before they
 * had been told what the choice was; showing all of them open put nine snippets
 * on screen and made the page look like it wanted nine things done. Closed
 * panels state the options in one line each, and the explanation of a way
 * arrives with the snippets of that way — the reader opens the one they mean to
 * use, and reads only that. None opens on its own: even without the browser
 * sign-in, two ways are left to choose from.
 *
 * The panels are `<details>` rather than a component: the project already opens
 * disclosures that way (the scope on the keys page, the cron help), and the
 * element carries the keyboard behaviour and the open state on its own. That
 * choice is what puts this file past 300 lines, and it is kept on purpose.
 *
 * The real content of the page is no panel but the warning each one carries:
 * whichever way an agent comes in, it reaches the whole instance.
 */
@Component({
  selector: 'app-agents',
  standalone: true,
  imports: [
    RouterLink,
    AuthorizedAgentsComponent,
    SnippetListComponent,
    AccountComponent,
    ThemeToggleComponent,
    ZardAlertComponent,
    ZardButtonComponent,
    ZardIconComponent,
  ],
  template: `
    <div class="mx-auto flex h-full w-full max-w-app flex-col">
      <header class="flex items-center justify-between border-b border-border px-4 py-2">
        <div class="flex items-center gap-3">
          <a z-button zType="ghost" zSize="sm" routerLink="/editor">
            <z-icon zType="arrow-left" class="h-4 w-4 md:mr-1.5" />
            <span class="max-md:sr-only">Functions</span>
          </a>
          <h1 class="text-lg font-semibold">AI Agent</h1>
        </div>
        <div class="flex items-center gap-2">
          <app-account [demoMode]="demoMode()" />
          <app-theme-toggle />
          <button z-button zType="outline" zSize="sm" (click)="logout()">
            <z-icon zType="log-out" class="h-4 w-4 md:mr-1.5" />
            <span class="max-md:sr-only">Sign out</span>
          </button>
        </div>
      </header>

      <div class="flex-1 overflow-y-auto p-4">
        @if (errorMessage()) {
          <z-alert
            class="mb-4"
            zType="destructive"
            zTitle="Error"
            [zDescription]="errorMessage()"
          />
        }

        <p class="mb-4 text-sm text-muted-foreground">
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
          instance, and is told how to write one when it connects. An agent with no MCP client does
          the same over plain HTTP with a key, once it has read
          <code class="font-mono text-foreground">{{ guide() }}</code
          >. Pick the way in.
        </p>

        @if (oauthAvailable() === null) {
          <p class="text-sm text-muted-foreground">Reading what this instance supports...</p>
        } @else {
          @if (oauthAvailable()) {
            <details class="mb-3 rounded-lg border border-border p-3">
              <summary
                class="cursor-pointer select-none text-sm font-semibold marker:text-muted-foreground"
              >
                Signin from your agent
                <span class="ml-1 text-xs font-normal text-muted-foreground">
                  — no key to paste, you click Authorize in this editor
                </span>
              </summary>

              <div class="mt-3 space-y-2 text-sm text-muted-foreground">
                <p>
                  Paste the snippet for your client, then run its authentication command. It opens
                  this editor, names itself, and waits for you to click
                  <strong class="text-foreground">Authorize</strong>. Nothing secret is ever pasted
                  anywhere, and the authorization expires on its own after ninety days.
                </p>
                <p>
                  What you authorize is <strong class="text-foreground">everything</strong>:
                  reading, writing, running and deleting every function of this instance, and
                  reading their execution history. There is nothing to tick — the consent screen
                  says so before you click. Cut an agent off from
                  <strong class="text-foreground">Authorized agents</strong>
                  below; it stops working on its next call.
                </p>
              </div>

              <div class="mt-3">
                <app-snippet-list [clients]="oauthClients()" />
              </div>
            </details>
          }

          <details class="mb-3 rounded-lg border border-border p-3">
            <summary
              class="cursor-pointer select-none text-sm font-semibold marker:text-muted-foreground"
            >
              Use an API key
              <span class="ml-1 text-xs font-normal text-muted-foreground">
                — for a client that cannot open a browser
              </span>
            </summary>

            <div class="mt-3 space-y-2 text-sm text-muted-foreground">
              <p>
                The key carries <strong class="text-foreground">Can manage functions</strong> and an
                <strong class="text-foreground">open scope</strong>: creating a function is refused
                to a restricted key, so an agent that has to create reaches every function here
                anyway. Create one on the
                <a routerLink="/keys" class="underline underline-offset-2 hover:text-foreground"
                  >API keys</a
                >
                page — give it an expiry — then replace
                <code class="font-mono text-foreground">{{ placeholder }}</code> below.
              </p>
              <p>
                The key then sits in your client's configuration file, in clear. That is the trade
                of this way: it works without a browser, and nothing expires it but the date you
                set.
                @if (!oauthAvailable()) {
                  Signing in from the agent is not available here, since this instance was not told
                  its own public address.
                }
              </p>
            </div>

            <div class="mt-3">
              <app-snippet-list [clients]="keyClients()" />
            </div>
          </details>

          <details class="mb-3 rounded-lg border border-border p-3">
            <summary
              class="cursor-pointer select-none text-sm font-semibold marker:text-muted-foreground"
            >
              Use a key without MCP
              <span class="ml-1 text-xs font-normal text-muted-foreground">
                — nothing to configure, the agent reads /llms.txt
              </span>
            </summary>

            <div class="mt-3 space-y-2 text-sm text-muted-foreground">
              <p>
                Nothing goes in your client's configuration: paste the prompt below into the
                conversation, with <code class="font-mono text-foreground">{{ placeholder }}</code>
                replaced. The agent reads
                <code class="font-mono text-foreground">{{ guide() }}</code
                >, which teaches it how to write a function and how to call this instance, then
                works over plain HTTP. It takes an agent that can send a request with a header of
                its own — Claude Code or Codex can, a web chat usually cannot.
              </p>
              <p>
                The key is the same as for MCP —
                <strong class="text-foreground">Can manage functions</strong>, an
                <strong class="text-foreground">open scope</strong>, an expiry — created on the
                <a routerLink="/keys" class="underline underline-offset-2 hover:text-foreground"
                  >API keys</a
                >
                page, and it reaches <strong class="text-foreground">everything</strong>: reading,
                writing, running and deleting every function of this instance, and reading their
                execution history.
              </p>
              <p>
                The trade of this way: the key goes
                <strong class="text-foreground">into the conversation</strong>, and with it into
                what is sent to the provider of the model and kept in your client's history. A key
                in an MCP configuration stays in the client.
              </p>
            </div>

            <div class="mt-3">
              <app-snippet-list [clients]="llmsClients()" />
            </div>
          </details>

          @if (oauthAvailable()) {
            <div class="mt-6 border-t border-border pt-4">
              <app-authorized-agents
                [grants]="grants()"
                [isLoading]="isLoadingGrants()"
                [demoMode]="demoMode()"
                (revoke)="onRevoke($event)"
              />
            </div>
          }
        }
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
export class AgentsComponent implements OnInit {
  private readonly agentsService = inject(AgentsService);
  private readonly authService = inject(AuthService);
  private readonly instance = inject(InstanceService);

  /**
   * A showcase: the authorized agents are listed, and cutting one off is
   * closed. The page reads the mode and hands it down.
   */
  protected readonly demoMode = this.instance.demoMode;

  /**
   * The name of this box, which is what the snippets below call the server. It
   * comes from the same service as the mode, read before the first render, so
   * no snippet is ever shown under a key it is about to lose.
   */
  protected readonly instanceName = this.instance.name;

  protected readonly placeholder = KEY_PLACEHOLDER;

  /**
   * The address is read from the browser, like the invoke banner does and for
   * the same reason: the snippet has to work as it stands on the instance it is
   * displayed from.
   */
  protected readonly endpoint = computed(() => `${window.location.origin}/mcp`);

  /** Where the guide for an agent with no MCP client is served, read the same way. */
  protected readonly guide = computed(() => llmsGuideUrl(window.location.origin));

  /**
   * Whether this instance can authorize an agent at all — `null` until the
   * probe has answered.
   *
   * Three states rather than two, and the third is what keeps the page still.
   * Starting at `false` would paint the page without its sign-in panel, then
   * insert one above the others the moment the probe came back — moving the
   * panel the reader was about to open. Nothing below is rendered until the
   * answer is in.
   */
  protected readonly oauthAvailable = signal<boolean | null>(null);

  protected readonly oauthClients = computed(() =>
    agentSnippets(this.endpoint(), false, this.instanceName()),
  );
  protected readonly keyClients = computed(() =>
    agentSnippets(this.endpoint(), true, this.instanceName()),
  );
  protected readonly llmsClients = computed(() => [llmsPrompt(window.location.origin)]);

  protected readonly grants = signal<FaasboxOAuthGrant[]>([]);
  protected readonly isLoadingGrants = signal(false);
  protected readonly errorMessage = signal('');

  async ngOnInit(): Promise<void> {
    const available = await firstValueFrom(this.agentsService.oauthAvailable());
    this.oauthAvailable.set(available);
    if (!available) return;

    await this.loadGrants();
  }

  private async loadGrants(): Promise<void> {
    this.isLoadingGrants.set(true);
    try {
      const res = await firstValueFrom(this.agentsService.listGrants());
      this.grants.set(res.items);
    } catch (e) {
      this.report(e, 'Failed to load the authorized agents');
    } finally {
      this.isLoadingGrants.set(false);
    }
  }

  protected async onRevoke(grant: FaasboxOAuthGrant): Promise<void> {
    if (this.demoMode()) return;
    const name = grantClientName(grant);
    if (
      !confirm(`Revoke "${name}"? It stops working on its next call and has to authorize again.`)
    ) {
      return;
    }
    this.errorMessage.set('');
    try {
      await firstValueFrom(this.agentsService.revoke(grant.id));
      this.grants.update((list) => list.filter((g) => g.id !== grant.id));
    } catch (e) {
      this.report(e, 'Failed to revoke the agent');
    }
  }

  protected logout(): void {
    this.authService.logout();
  }

  private report(error: unknown, fallback: string): void {
    const message = error instanceof Error ? error.message : '';
    this.errorMessage.set(message ? `${fallback}: ${message}` : fallback);
  }
}
