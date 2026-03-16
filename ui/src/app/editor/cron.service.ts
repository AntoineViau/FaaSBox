import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

import type { FaasboxCronJob, FaasboxCronJobListResponse } from '@/models/faasbox-cron-job.model';

const BASE_URL = '/api/collections/faasbox_cron_jobs/records';

@Injectable({ providedIn: 'root' })
export class CronService {
  private readonly http = inject(HttpClient);

  list(functionName: string) {
    return this.http.get<FaasboxCronJobListResponse>(BASE_URL, {
      params: { filter: `functionName='${functionName}'`, sort: '-name', perPage: '200' },
    });
  }

  listAll() {
    return this.http.get<FaasboxCronJobListResponse>(BASE_URL, {
      params: { perPage: '200' },
    });
  }

  create(data: Partial<FaasboxCronJob>) {
    return this.http.post<FaasboxCronJob>(BASE_URL, data);
  }

  update(id: string, data: Partial<FaasboxCronJob>) {
    return this.http.patch<FaasboxCronJob>(`${BASE_URL}/${id}`, data);
  }

  delete(id: string) {
    return this.http.delete<void>(`${BASE_URL}/${id}`);
  }
}
