export interface FaasboxTrigger {
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
   * What this trigger is: a schedule, a run at startup, or a public route that
   * serves the function at /f/ on the pages host. Empty reads as 'cron'
   * server-side, which is the shape a record written from the PocketBase admin
   * carries.
   */
  kind: 'cron' | 'startup' | 'route' | '';
  /** On a startup trigger, how long after boot it fires. 0 to 1439. */
  startupDelayMinutes: number;
  /**
   * The IANA zone the cron expression is read in. Empty reads as 'UTC'
   * server-side, which is the shape a record written from the PocketBase admin
   * carries. Ignored on a startup trigger and on a route.
   */
  timezone: string;
  created: string;
  updated: string;
}

export interface FaasboxTriggerListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: FaasboxTrigger[];
}
