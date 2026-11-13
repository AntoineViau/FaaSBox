import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';

/**
 * The route branch of a trigger card: where the function is published, and what
 * that means.
 *
 * Presentational: it owns no state and writes nothing. A route carries no field
 * of its own — no schedule, no payload, no delay — so all there is to show is
 * the address it opens and the warning that goes with it.
 *
 * The address is built on the name the function is saved under, not on what is
 * being typed in the name field: /f/ resolves what the database holds. The id
 * would survive a rename, the name reads better in a link; both are served.
 *
 * `pagesUrl` empty means the server serves no page at all — no
 * FAASBOX_PAGES_URL, or a showcase. A route already stored is then shown as
 * what it is: kept, and not served.
 */
@Component({
  selector: 'app-route-trigger',
  standalone: true,
  template: `
    @if (pagesUrl()) {
      <div>
        <label class="mb-1 block text-xs text-muted-foreground">Public URL</label>
        <a
          [href]="url()"
          target="_blank"
          rel="noopener noreferrer"
          class="break-all font-mono text-sm underline underline-offset-2 hover:text-primary"
          >{{ url() }}</a
        >
        @if (!active()) {
          <p class="mt-0.5 text-xs text-muted-foreground">
            Inactive: this address answers 404 until the trigger is switched on and saved.
          </p>
        }
      </div>
      <p
        class="rounded-md border border-yellow-500/30 bg-yellow-500/10 px-2 py-1.5 text-xs text-yellow-600 dark:text-yellow-500"
      >
        Anyone who has this address can run this function — no API key, no session. Its stdout
        must be a response envelope (status, headers, body), and whatever it echoes from the
        request has to be escaped.
      </p>
    } @else {
      <p
        class="rounded-md border border-yellow-500/30 bg-yellow-500/10 px-2 py-1.5 text-xs text-yellow-600 dark:text-yellow-500"
      >
        Not served: public pages are off on this server (FAASBOX_PAGES_URL is not set). The
        trigger is kept, and starts serving once they are turned on.
      </p>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RouteTriggerComponent {
  /** The saved name of the function the route publishes. */
  readonly functionName = input.required<string>();
  /** The origin of the public pages, or '' when the server serves none. */
  readonly pagesUrl = input.required<string>();
  readonly active = input.required<boolean>();

  protected readonly url = computed(
    () => `${this.pagesUrl()}/f/${encodeURIComponent(this.functionName())}`,
  );
}
