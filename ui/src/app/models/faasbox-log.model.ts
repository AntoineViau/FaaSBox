export interface FaasboxLog {
  id: string;
  /** Id of the function the entry belongs to. Filtering and grouping key. */
  function: string;
  /** What that function was called when the entry was written, not its current name. */
  functionName: string;
  trigger: 'http' | 'cron' | 'startup';
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
