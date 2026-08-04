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

  list(functionName: string) {
    const filter = encodeURIComponent(`functionName='${functionName}'`);
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
    functionName: string,
    onNewLog: (log: FaasboxLog) => void,
    onReconnect?: () => void,
  ): () => void {
    return this.realtime.connect({
      topics: [LOGS_TOPIC],
      onMessage: (_topic, data) => {
        const event = data as { action?: string; record?: FaasboxLog };
        if (event.action === 'create' && event.record?.functionName === functionName) {
          onNewLog(event.record);
        }
      },
      onReconnect,
    });
  }
}
