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
  /**
   * Which deadline fires this trigger. Empty reads as 'cron' server-side, which
   * is the shape a record written from the PocketBase admin carries.
   */
  kind: 'cron' | 'startup' | '';
  /** On a startup trigger, how long after boot it fires. 0 to 1439. */
  startupDelayMinutes: number;
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
