export interface FaasboxLog {
  id: string;
  functionName: string;
  trigger: 'http' | 'cron';
  status: 'success' | 'error' | 'timeout' | 'missed';
  duration: number;
  stdout: string;
  stderr: string;
  requestPayload: unknown;
  exitCode: number;
  created: string;
  updated: string;
}

export interface FaasboxLogListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: FaasboxLog[];
}
