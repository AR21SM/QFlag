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

export type AuditLog = {
  flagName: string;
  action: string;
  oldValue?: unknown;
  newValue?: unknown;
  updatedBy: string;
  timestamp: string;
  version: number;
};

export type ClusterStatus = {
  leader: string;
  term: number;
  commitIndex: number;
  healthyNodes: number;
  totalNodes: number;
  leaderChanges: number;
  avgCommitLatency: string;
  nodes: Array<{
    id: string;
    role: "leader" | "candidate" | "follower";
    healthy: boolean;
    term: number;
    commitIndex: number;
    lastApplied: number;
    lastHeartbeatAt: string;
    url: string;
  }>;
};
