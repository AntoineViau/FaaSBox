import { inject, Injectable, NgZone } from '@angular/core';

import { AuthService } from '@/auth/auth.service';

const REALTIME_URL = '/api/realtime';

/**
 * How long to wait before each reconnection attempt, in milliseconds. The last
 * value is the ceiling — attempts past it keep that spacing rather than growing
 * further. Retrying tightly against a server that is down is a denial of service
 * one inflicts on oneself, and a fixed short delay is exactly that.
 */
const RECONNECT_DELAYS_MS = [1_000, 2_000, 5_000, 10_000, 30_000];

export interface RealtimeSubscription {
  /** PocketBase subscription names to register for. */
  topics: string[];
  /** Called for every message arriving on one of the topics. */
  onMessage: (topic: string, data: unknown) => void;
  /**
   * Called after a *re*connection, never after the first one. Messages emitted
   * while the stream was down are lost, so a consumer whose state is not a
   * running tally has to re-read it here.
   */
  onReconnect?: () => void;
}

/**
 * The PocketBase realtime protocol, written once.
 *
 * The sequence is fixed: open an EventSource on /api/realtime, read the
 * clientId off the PB_CONNECT event, then POST it back with the list of
 * subscriptions and the auth token. Registering replaces the client's whole
 * subscription set, so a second consumer cannot add to the first one's list —
 * which is why each connect() call opens its own stream and gets its own
 * clientId. Sharing one connection would be tidier and remains possible later;
 * what must not be written twice is the protocol.
 *
 * The service is callback-based rather than signal-based. That is a deliberate
 * exception to the project's "signals everywhere" rule, inherited from the log
 * stream it replaces: an EventSource is a push source with a lifetime, not a
 * value. It is not a licence to widen the exception elsewhere.
 */
@Injectable({ providedIn: 'root' })
export class RealtimeService {
  private readonly authService = inject(AuthService);
  private readonly ngZone = inject(NgZone);

  /** Opens a stream and returns the function that closes it for good. */
  connect(subscription: RealtimeSubscription): () => void {
    let eventSource: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let attempt = 0;
    let hasConnectedOnce = false;
    let closed = false;

    const scheduleReconnect = () => {
      if (closed || retryTimer !== null) return;
      const delay = RECONNECT_DELAYS_MS[Math.min(attempt, RECONNECT_DELAYS_MS.length - 1)];
      attempt++;
      // Kept visible on purpose: a stream that drops every few seconds would
      // otherwise recover silently while the live display it feeds is a fiction.
      console.warn(`faasbox: realtime stream lost, retrying in ${delay}ms`);
      retryTimer = setTimeout(() => {
        retryTimer = null;
        open();
      }, delay);
    };

    // Takes the stream it is reacting to: a dying one can still fire its error
    // after a replacement is open, and closing `eventSource` blindly would kill
    // the good connection instead of the dead one.
    const dropAndRetry = (source: EventSource) => {
      if (source !== eventSource) return;
      source.close();
      eventSource = null;
      scheduleReconnect();
    };

    const open = () => {
      if (closed) return;
      const source = new EventSource(REALTIME_URL);
      eventSource = source;

      source.addEventListener('PB_CONNECT', async (e: MessageEvent) => {
        if (closed) return;
        try {
          const { clientId } = JSON.parse(e.data);
          const token = this.authService.getToken();
          const res = await fetch(REALTIME_URL, {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
              ...(token ? { Authorization: token } : {}),
            },
            body: JSON.stringify({ clientId, subscriptions: subscription.topics }),
          });
          if (!res.ok) throw new Error(`subscription refused with ${res.status}`);
        } catch (err) {
          if (closed) return;
          console.warn('faasbox: realtime subscription failed', err);
          dropAndRetry(source);
          return;
        }
        if (closed) return;

        // Only reset the backoff once the stream proved usable: a server that
        // accepts the connection then drops it must still be backed off from.
        attempt = 0;
        const reconnected = hasConnectedOnce;
        hasConnectedOnce = true;
        if (reconnected) {
          console.info('faasbox: realtime stream reconnected');
          this.ngZone.run(() => subscription.onReconnect?.());
        }
      });

      for (const topic of subscription.topics) {
        source.addEventListener(topic, (e: MessageEvent) => {
          if (closed) return;
          this.ngZone.run(() => subscription.onMessage(topic, JSON.parse(e.data)));
        });
      }

      // EventSource retries on its own, at a fixed interval it does not expose.
      // Closing here takes the retry over so the spacing is the one above.
      source.addEventListener('error', () => {
        if (closed) return;
        dropAndRetry(source);
      });
    };

    open();

    return () => {
      closed = true;
      if (retryTimer !== null) clearTimeout(retryTimer);
      eventSource?.close();
      eventSource = null;
    };
  }
}
