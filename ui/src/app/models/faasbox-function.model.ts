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

/** What the server pushes on the dependency state channel. */
export interface DepsStateMessage {
  functionName: string;
  depsStatus: DepsStatus;
  depsError: string;
}
