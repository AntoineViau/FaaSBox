import { HttpClient } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';

import type { FaasboxLog, FaasboxLogListResponse } from '@/models/faasbox-log.model';
import { RealtimeService } from '@/editor/realtime.service';

const BASE_URL = '/api/collections/faasbox_logs/records';
const LOGS_TOPIC = 'faasbox_logs';

@Injectable({ providedIn: 'root' })
export class LogsService {
  private readonly http = inject(HttpClient);
  private readonly realtime = inject(RealtimeService);

  /**
   * Entries of one function, keyed on its id. Filtering on the stored name would
   * split the history in two at the first rename: an entry keeps the name the
   * function had when it ran, on purpose.
   */
  list(functionId: string) {
    const filter = encodeURIComponent(`function='${functionId}'`);
    return this.http.get<FaasboxLogListResponse>(
      `${BASE_URL}?filter=${filter}&sort=-created&perPage=50`,
    );
  }

  /**
   * Streams the log entries of one function. onReconnect fires after a dropped
   * stream comes back: entries written during the gap were never delivered, so
   * the list has to be re-read rather than continued.
   */
  subscribe(
    functionId: string,
    onNewLog: (log: FaasboxLog) => void,
    onReconnect?: () => void,
  ): () => void {
    return this.realtime.connect({
      topics: [LOGS_TOPIC],
      onMessage: (_topic, data) => {
        const event = data as { action?: string; record?: FaasboxLog };
        if (event.action === 'create' && event.record?.function === functionId) {
          onNewLog(event.record);
        }
      },
      onReconnect,
    });
  }
}
