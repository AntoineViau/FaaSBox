export interface FaasboxCronJob {
  id: string;
  name: string;
  schedule: string;
  /** Id of the target function — a relation, so a rename does not break it. */
  function: string;
  payload: unknown;
  active: boolean;
  /** Max simultaneous executions (waiting + running). 0 means no limit. */
  maxQueue: number;
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
