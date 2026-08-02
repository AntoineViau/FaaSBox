import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

import type {
  FaasboxApiKey,
  FaasboxApiKeyCreated,
  FaasboxApiKeyListResponse,
} from '@/models/faasbox-api-key.model';

const BASE_URL = '/api/collections/faasbox_api_keys/records';

/** Dedicated endpoint: the only path that returns the raw key value. */
const CREATE_URL = '/api/faasbox/keys';

@Injectable({ providedIn: 'root' })
export class ApiKeysService {
  private readonly http = inject(HttpClient);

  list() {
    return this.http.get<FaasboxApiKeyListResponse>(BASE_URL, {
      params: { sort: '-created', perPage: '200' },
    });
  }

  create(name: string, allowedFunctions: string[], expiresAt: string) {
    return this.http.post<FaasboxApiKeyCreated>(CREATE_URL, {
      name,
      allowedFunctions,
      expiresAt,
    });
  }

  update(id: string, data: Partial<FaasboxApiKey>) {
    return this.http.patch<FaasboxApiKey>(`${BASE_URL}/${id}`, data);
  }

  delete(id: string) {
    return this.http.delete<void>(`${BASE_URL}/${id}`);
  }
}
