export type FeatureFlag = {
  flagName: string;
  projectId: string;
  environment: string;
  enabled: boolean;
  rolloutPercentage: number;
  targetUsers: string[];
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type Evaluation = {
  flag: string;
  enabled: boolean;
  reason: string;
  version: number;
};

export type QFlagOptions = {
  endpoints: string[] | string;
  projectId: string;
  environment: string;
  apiToken?: string;
  fetchImpl?: typeof fetch;
  cacheTtlMs?: number;
  now?: () => number;
};

export class QFlag {
  private readonly endpoints: string[];
  private readonly projectId: string;
  private readonly environment: string;
  private readonly apiToken?: string;
  private readonly fetchImpl: typeof fetch;
  private readonly cacheTtlMs: number;
  private readonly now: () => number;
  private readonly cache = new Map<string, { flag: FeatureFlag; cachedAt: number }>();

  constructor(options: QFlagOptions) {
    const endpoints = Array.isArray(options.endpoints) ? options.endpoints : [options.endpoints];
    this.endpoints = endpoints.map((endpoint) => endpoint.replace(/\/$/, ""));
    this.projectId = options.projectId;
    this.environment = options.environment;
    this.apiToken = options.apiToken;
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.cacheTtlMs = options.cacheTtlMs ?? 30_000;
    this.now = options.now ?? Date.now;
    if (!Number.isFinite(this.cacheTtlMs) || this.cacheTtlMs < 0) {
      throw new Error("cacheTtlMs must be a non-negative number");
    }
  }

  async refresh(): Promise<FeatureFlag[]> {
    const flags = await this.request<FeatureFlag[]>(`/api/v1/projects/${this.projectId}/env/${this.environment}/flags`);
    this.cache.clear();
    for (const flag of flags) {
      this.cache.set(flag.flagName, { flag, cachedAt: this.now() });
    }
    return flags;
  }

  async evaluate(flagName: string, userId: string, defaultValue = false): Promise<Evaluation> {
    const cached = this.cache.get(flagName);
    if (cached && this.now() - cached.cachedAt <= this.cacheTtlMs) {
      return evaluateLocal(cached.flag, userId, defaultValue);
    }

    try {
      return await this.request<Evaluation>(
        `/api/v1/projects/${this.projectId}/env/${this.environment}/flags/${flagName}/evaluate?userId=${encodeURIComponent(
          userId,
        )}&default=${defaultValue}`,
      );
    } catch {
      return {
        flag: flagName,
        enabled: defaultValue,
        reason: "default",
        version: 0,
      };
    }
  }

  async isEnabled(flagName: string, userId: string, defaultValue = false): Promise<boolean> {
    return (await this.evaluate(flagName, userId, defaultValue)).enabled;
  }

  upsertCachedFlag(flag: FeatureFlag): void {
    this.cache.set(flag.flagName, { flag, cachedAt: this.now() });
  }

  private async request<T>(path: string): Promise<T> {
    let lastError: unknown;

    for (const endpoint of this.endpoints) {
      try {
        const response = await this.fetchImpl(endpoint + path, {
          headers: this.apiToken ? { Authorization: `Bearer ${this.apiToken}` } : undefined,
        });
        if (!response.ok) {
          lastError = new Error(`request failed: ${response.status}`);
          continue;
        }
        return (await response.json()) as T;
      } catch (error) {
        lastError = error;
      }
    }

    throw lastError instanceof Error ? lastError : new Error("request failed");
  }
}

export function evaluateLocal(flag: FeatureFlag, userId: string, defaultValue = false): Evaluation {
  if (!flag) {
    return { flag: "", enabled: defaultValue, reason: "default", version: 0 };
  }

  if (flag.targetUsers.includes(userId)) {
    return { flag: flag.flagName, enabled: true, reason: "target_user", version: flag.version };
  }

  if (!flag.enabled) {
    return { flag: flag.flagName, enabled: false, reason: "flag_disabled", version: flag.version };
  }

  const bucket = rolloutBucket(userId, flag.flagName);
  const enabled = bucket < flag.rolloutPercentage;
  return {
    flag: flag.flagName,
    enabled,
    reason: enabled ? "percentage_rollout" : "percentage_rollout_miss",
    version: flag.version,
  };
}

export function rolloutBucket(userId: string, flagName: string): number {
  let hash = 2166136261;
  const input = `${userId}:${flagName}`;
  for (let index = 0; index < input.length; index += 1) {
    hash ^= input.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return Math.abs(hash >>> 0) % 100;
}
