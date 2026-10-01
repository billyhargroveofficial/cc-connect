export interface TelegramBinding {
  enabled: boolean;
  tokenEnv: string;
  allowedUserIds?: string[];
  username?: string;
  status?: string;
  error?: string;
}
export interface Bot {
  id: string;
  name: string;
  role: string;
  avatar: string;
  chief: boolean;
  backend: string;
  model: string;
  effort: string;
  workDir: string;
  status: string;
  threads: Record<string, string>;
  disabledSkills: string[];
  telegram?: TelegramBinding;
  createdAt: string;
  updatedAt: string;
}
export interface Event {
  seq: number;
  botId: string;
  turnId?: string;
  type: string;
  time: string;
  data: Record<string, unknown>;
}
export type StudioEvent = Event;
export interface Attachment {
  id: string;
  name: string;
  mimeType: string;
  path?: string;
  url?: string;
}
export interface Model {
  id: string;
  name: string;
  backend: string;
  efforts: string[];
}
export interface BackendCapabilities {
  available: boolean;
  reason?: string;
  goals: boolean;
  subagents: boolean;
}
export interface Capabilities {
  models: Model[];
  voice: boolean;
  backends: Record<string, BackendCapabilities>;
}
export interface Skill {
  id: string;
  name: string;
  description: string;
  path: string;
  scope: string;
  enabled: boolean;
  editable: boolean;
}
export interface Instructions {
  content: string;
  path: string;
}
export interface Goal {
  objective?: string;
  status?: string;
  tokenBudget?: number | null;
  tokensUsed?: number;
  [key: string]: unknown;
}
export interface GoalSnapshot {
  goal: Goal | null;
  cursor: number;
}
export interface BotContext {
  compacting: boolean;
  estimated?: boolean;
  usedTokens?: number;
  contextWindow?: number;
  remainingTokens?: number;
  percent?: number;
}
export interface Maintenance {
  enabled?: boolean;
  hour?: number;
  model?: string;
  backend?: string;
  effort?: string;
  retentionDays?: number;
  runs?: MaintenanceRun[];
  reports?: MaintenanceRun[];
  [key: string]: unknown;
}
export interface MaintenanceRun {
  id?: string;
  botId?: string;
  botName?: string;
  time?: string;
  startedAt?: string;
  completedAt?: string;
  status?: string;
  summary?: string;
  error?: string;
  [key: string]: unknown;
}
