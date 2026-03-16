export interface FaasboxCronJob {
  id: string;
  name: string;
  schedule: string;
  functionName: string;
  payload: unknown;
  active: boolean;
  created: string;
  updated: string;
}

export interface FaasboxCronJobListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: FaasboxCronJob[];
}
