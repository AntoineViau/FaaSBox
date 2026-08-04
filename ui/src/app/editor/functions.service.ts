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

  list() {
    return this.http.get<FaasboxFunctionListResponse>(BASE_URL, {
      params: { sort: 'name', perPage: '200' },
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

  invoke(name: string, payload: unknown) {
    return this.http.post<InvocationResult>(`/invoke/${name}`, payload);
  }

  /** Decrypted secrets of a function. Superuser only, and the only way to read them back. */
  getEnv(name: string) {
    return this.http.get<Record<string, string>>(`/api/faasbox/functions/${name}/env`);
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
