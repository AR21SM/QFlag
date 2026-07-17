import type { AuditLog, ClusterStatus, Evaluation, FeatureFlag } from "@/lib/types";

const DEFAULT_PROJECT = "project_123";
const DEFAULT_ENV = "prod";

type RequestOptions = RequestInit & {
  retryLeader?: boolean;
};

export class ApiClient {
  readonly endpoints: string[];

  constructor(endpoints = "/api/raft") {
    this.endpoints = endpoints
      .split(",")
      .map((endpoint) => endpoint.trim().replace(/\/$/, ""))
      .filter(Boolean);
  }

  listFlags() {
    return this.request<FeatureFlag[]>(`/api/v1/projects/${DEFAULT_PROJECT}/env/${DEFAULT_ENV}/flags`);
  }

  createFlag(input: Pick<FeatureFlag, "flagName" | "enabled" | "rolloutPercentage" | "targetUsers">) {
    return this.request<FeatureFlag>(`/api/v1/projects/${DEFAULT_PROJECT}/env/${DEFAULT_ENV}/flags`, {
      method: "POST",
      body: JSON.stringify(input),
    });
  }

  updateFlag(flagName: string, patch: Partial<Pick<FeatureFlag, "enabled" | "rolloutPercentage" | "targetUsers">>) {
    return this.request<FeatureFlag>(`/api/v1/projects/${DEFAULT_PROJECT}/env/${DEFAULT_ENV}/flags/${flagName}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }

  deleteFlag(flagName: string) {
    return this.request<void>(`/api/v1/projects/${DEFAULT_PROJECT}/env/${DEFAULT_ENV}/flags/${flagName}`, {
      method: "DELETE",
    });
  }

  rollback(flagName: string, version: number) {
    return this.request<FeatureFlag>(`/api/v1/projects/${DEFAULT_PROJECT}/env/${DEFAULT_ENV}/flags/${flagName}/rollback`, {
      method: "POST",
      body: JSON.stringify({ version }),
    });
  }

  evaluate(flagName: string, userId: string) {
    return this.request<Evaluation>(
      `/api/v1/projects/${DEFAULT_PROJECT}/env/${DEFAULT_ENV}/flags/${flagName}/evaluate?userId=${encodeURIComponent(userId)}`,
    );
  }

  clusterStatus() {
    return this.request<ClusterStatus>("/api/v1/cluster/status");
  }

  audit() {
    return this.request<AuditLog[]>(`/api/v1/audit?projectId=${DEFAULT_PROJECT}&env=${DEFAULT_ENV}`);
  }

  metrics() {
    return this.requestText("/metrics");
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await this.fetchFromAny(path, options);
    return (await response.json()) as T;
  }

  private async requestText(path: string): Promise<string> {
    const response = await this.fetchFromAny(path);
    return response.text();
  }

  private async fetchFromAny(path: string, options: RequestOptions = {}): Promise<Response> {
    let lastError: unknown;
    for (const endpoint of this.endpoints) {
      try {
        const response = await fetch(endpoint + path, {
          ...options,
          headers: {
            "Content-Type": "application/json",
            "X-Actor": "dashboard",
            ...options.headers,
          },
        });

        if (response.ok) {
          return response;
        }

        lastError = new Error(`${response.status} ${response.statusText}`);
      } catch (error) {
        lastError = error;
      }
    }

    throw lastError instanceof Error ? lastError : new Error("QFlag API unavailable");
  }
}
