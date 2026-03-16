export interface FaasboxFunction {
  id: string;
  name: string;
  script: string;
  packageJson: string;
  env: string;
  plainEnv: Record<string, string> | null;
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
