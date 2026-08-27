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
  /**
   * The call the editor's Runner replays for this function: the body as typed,
   * and the headers as JSON text. They belong to the record so that switching
   * function loads them like everything else — and an empty value is not a
   * value, it reads back as the starting sample.
   */
  sampleBody: string;
  sampleHeaders: string;
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
 * `functionId` is always set and is the only thing to match on: it survives a
 * rename, which the name does not. `functionName` is there for whoever reads
 * the stream, never for identification.
 */
export interface DepsStateMessage {
  functionId: string;
  functionName: string;
  depsStatus: DepsStatus;
  depsError: string;
}
