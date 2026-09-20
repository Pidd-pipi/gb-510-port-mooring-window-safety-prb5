
export interface ClearanceInterlock {
  planCode: string;
  planStatus: string;
  planApproved: boolean;
  windowCode: string;
  windowStatus: string;
  windowSafe: boolean;
  expectedWindowVersion: number;
  currentWindowVersion: number;
  windowVersionMatch: boolean;
  satisfied: boolean;
  invalidReason: string;
}

export interface DomainRecord {
  id: number;
  code: string;
  name: string;
  status: string;
  version: number;
  description: string;
  facility: string;
  owner: string;
  category: string;
  riskLevel: 'low' | 'medium' | 'high' | 'critical';
  metricValue: number;
  metricUnit: string;
  effectiveAt: string;
  evidence: string;
  relatedCode: string;
  planCode?: string;
  windowCode?: string;
  windowVersion?: number;
  submittedBy?: string;
  submittedAt?: string;
  confirmedBy?: string;
  confirmedAt?: string;
  interlock?: ClearanceInterlock;
  createdAt: string;
  updatedAt: string;
}

export interface PageMeta { page: number; pageSize: number; total: number }
export interface ApiEnvelope<T> { data: T; error?: string; message?: string; meta?: PageMeta }
export interface UserSession { token: string; username: string; displayName: string; role: string; expiresIn: number }
export interface AuditLog {
  id: number; requestId: string; actor: string; action: string; entityType: string;
  entityId: number; beforeState: string; afterState: string; windowVersion?: number; detail: string; createdAt: string;
}
export interface EntityConfig {
  key: string;
  path: string;
  label: string;
  statuses: readonly string[];
  demoLinks?: { planCode: string; windowCode: string };
}
