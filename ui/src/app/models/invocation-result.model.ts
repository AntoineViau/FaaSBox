export interface InvocationResult {
  function: string;
  result?: unknown;
  error?: string;
  stdout?: string;
  stderr?: string;
  duration_ms: number;
  truncated?: boolean;
}
