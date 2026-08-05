/**
 * Where the dependency install stands. The empty value is a state of its own:
 * the function declares no package.json at all.
 */
export type DepsStatus = '' | 'pending' | 'installing' | 'ready' | 'error';

export interface FaasboxFunction {
  id: string;
  name: string;
  script: string;
  packageJson: string;
  env: string;
  plainEnv: Record<string, string> | null;
  depsStatus: DepsStatus;
  depsError: string;
  created: string;
  updated: string;
}

export interface FaasboxFunctionListResponse {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: FaasboxFunction[];
}

/**
 * What the server pushes on the dependency state channel.
 *
 * `functionId` is empty when the message comes from the invocation path, which
 * never loads the record. Match on it when it is set — it survives a rename —
 * and fall back to `functionName` when it is not.
 */
export interface DepsStateMessage {
  functionId: string;
  functionName: string;
  depsStatus: DepsStatus;
  depsError: string;
}
