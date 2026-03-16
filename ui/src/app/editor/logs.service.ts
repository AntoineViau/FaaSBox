import { HttpClient } from '@angular/common/http';
import { inject, Injectable, NgZone } from '@angular/core';

import { AuthService } from '@/auth/auth.service';
import type { FaasboxLog, FaasboxLogListResponse } from '@/models/faasbox-log.model';

const BASE_URL = '/api/collections/faasbox_logs/records';

@Injectable({ providedIn: 'root' })
export class LogsService {
  private readonly http = inject(HttpClient);
  private readonly authService = inject(AuthService);
  private readonly ngZone = inject(NgZone);

  list(functionName: string) {
    const filter = encodeURIComponent(`functionName='${functionName}'`);
    return this.http.get<FaasboxLogListResponse>(
      `${BASE_URL}?filter=${filter}&sort=-created&perPage=50`,
    );
  }

  subscribe(functionName: string, onNewLog: (log: FaasboxLog) => void): () => void {
    let eventSource: EventSource | null = null;
    let closed = false;

    const setup = () => {
      eventSource = new EventSource('/api/realtime');

      eventSource.addEventListener('PB_CONNECT', async (e: MessageEvent) => {
        if (closed) return;
        const data = JSON.parse(e.data);
        const clientId = data.clientId;

        const token = this.authService.getToken();
        await fetch('/api/realtime', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            ...(token ? { Authorization: token } : {}),
          },
          body: JSON.stringify({
            clientId,
            subscriptions: ['faasbox_logs'],
          }),
        });
      });

      eventSource.addEventListener('faasbox_logs', (e: MessageEvent) => {
        if (closed) return;
        const data = JSON.parse(e.data);
        if (data.action === 'create' && data.record?.functionName === functionName) {
          this.ngZone.run(() => onNewLog(data.record as FaasboxLog));
        }
      });
    };

    setup();

    return () => {
      closed = true;
      eventSource?.close();
    };
  }
}
