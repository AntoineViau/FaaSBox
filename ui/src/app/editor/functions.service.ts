import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

import type {
  DepsStateMessage,
  FaasboxFunction,
  FaasboxFunctionListResponse,
} from '@/models/faasbox-function.model';
import type { InvocationResult } from '@/models/invocation-result.model';
import { RealtimeService } from '@/editor/realtime.service';

const BASE_URL = '/api/collections/faasbox_functions/records';

/** Server-side subscription name carrying dependency installation states. */
const DEPS_TOPIC = 'faasbox_deps';

@Injectable({ providedIn: 'root' })
export class FunctionsService {
  private readonly http = inject(HttpClient);
  private readonly realtime = inject(RealtimeService);

  /**
   * Every function, with the fields the editor consumes spelled out. The store
   * calls this on load *and* on every realtime reconnection, so pulling the whole
   * collection would ship each function's lockfile down the wire on every resumed
   * connection — a payload nobody looks at. Spelling out what we want also drops
   * `env` and `plainEnv`: the Environment tab reads the secrets through its own
   * route.
   *
   * The cost of enumerating is that a field added later stays absent here until
   * someone adds it. PocketBase offers no exclusion, and the alternative is
   * paying for the lockfiles. `nameHash` is deliberately not asked for: it is a
   * fingerprint the server looks rows up by, and nothing on screen has any use
   * for it.
   *
   * **No sort is asked for.** The name is encrypted at rest, so a SQL sort would
   * order ciphertext — which is to say noise, and no choice of cipher fixes
   * that. The order is settled by each caller once the list is in hand, on the
   * plaintext the server decrypted for the response. The list is capped and
   * fully loaded, so it costs nothing.
   */
  list() {
    return this.http.get<FaasboxFunctionListResponse>(BASE_URL, {
      params: {
        perPage: '200',
        fields: 'id,name,script,packageJson,depsStatus,depsError,created,updated',
      },
    });
  }

  create(data: Pick<FaasboxFunction, 'name' | 'script' | 'packageJson'> & { plainEnv?: Record<string, string> }) {
    return this.http.post<FaasboxFunction>(BASE_URL, data);
  }

  update(id: string, data: Partial<Pick<FaasboxFunction, 'name' | 'script' | 'packageJson'> & { plainEnv?: Record<string, string> }>) {
    return this.http.patch<FaasboxFunction>(`${BASE_URL}/${id}`, data);
  }

  delete(id: string) {
    return this.http.delete<void>(`${BASE_URL}/${id}`);
  }

  /**
   * Invokes through the very route an outside caller uses, carrying the body
   * and the headers it was given.
   *
   * The body is a **string** and travels as one: handing an object over would
   * have HttpClient serialise it, and what the function received would be a
   * document nobody wrote — which no signature computed over the original
   * bytes would survive. The server builds the envelope around it, exactly as
   * it does for a webhook arriving from outside.
   */
  invoke(name: string, body: string, headers: Record<string, string>) {
    return this.http.post<InvocationResult>(`/invoke/${name}`, body, { headers });
  }

  /**
   * Decrypted secrets of a function. Superuser only, and the only way to read
   * them back. The segment is the id: the route takes either spelling, and the
   * id is the one a rename in flight cannot invalidate.
   */
  getEnv(functionId: string) {
    return this.http.get<Record<string, string>>(`/api/faasbox/functions/${functionId}/env`);
  }

  /**
   * Streams dependency installation states for every function. The install runs
   * in the background for up to a minute after the save answered, so the save
   * response alone would show one frozen value — this is what makes the display
   * live without polling for it.
   */
  subscribeDepsState(
    onState: (state: DepsStateMessage) => void,
    onReconnect: () => void,
  ): () => void {
    return this.realtime.connect({
      topics: [DEPS_TOPIC],
      onMessage: (_topic, data) => onState(data as DepsStateMessage),
      onReconnect,
    });
  }
}
