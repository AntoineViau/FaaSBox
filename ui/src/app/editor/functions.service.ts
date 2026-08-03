import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

import type { FaasboxFunction, FaasboxFunctionListResponse } from '@/models/faasbox-function.model';
import type { InvocationResult } from '@/models/invocation-result.model';

const BASE_URL = '/api/collections/faasbox_functions/records';

@Injectable({ providedIn: 'root' })
export class FunctionsService {
  private readonly http = inject(HttpClient);

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
}
