// Shared API types mirroring the backend's JSON responses.

export interface ArtifactSummary {
  guid: string;
  kind: 'entry' | 'document' | 'link' | 'comment';
  type: string;
  title: string;
  hid: string;
  repoPath: string;
  parentPath: string;
  updatedCommit: string;
  linkCount: number;
  commentCount: number;
  content: ArtifactContent | null;
}

export interface ArtifactContent {
  guid: string;
  kind: string;
  type?: string;
  title?: string;
  hid?: string;
  base?: string;
  fields?: Record<string, unknown>;
  workflows?: Record<string, string>;
  sections?: DocSection[];
  subject?: string;
  parent?: string;
  author?: string;
  text?: string;
  createdAt?: string;
  source?: string;
  target?: string;
}

export interface DocBlock {
  type: 'text' | 'image' | 'entryRef';
  text?: string;
  attachment?: string;
  guid?: string;
}

export interface DocSection {
  id: string;
  heading?: string;
  blocks?: DocBlock[];
  children?: DocSection[];
}

export interface FieldDef {
  id: string;
  displayName?: string;
  type: string;
  multiple?: boolean;
  required?: boolean;
  indexed?: boolean;
  enum?: string;
  options?: string[];
  presentation?: Record<string, unknown>;
}

export interface RelationshipDef {
  linkType: string;
  displayName?: string;
  sourceTypes?: string[];
  targetTypes?: string[];
  cardinality?: string;
}

export interface EffectiveSchema {
  artifactType: string;
  kind: string;
  displayName: string;
  hidPrefix?: string;
  fields: FieldDef[];
  workflows: string[];
  relationships: RelationshipDef[];
  enums?: Record<string, { values: string[]; extendable?: boolean }>;
  presentation?: Record<string, unknown>;
  sources: string[];
}

export interface WorkflowState { id: string; displayName?: string }
export interface WorkflowTransition { name?: string; from: string; to: string }
export interface WorkflowDef {
  name: string;
  displayName?: string;
  initial: string;
  states: WorkflowState[];
  transitions: WorkflowTransition[];
}

export interface ArtifactWorkflow {
  definition: WorkflowDef;
  state: string;
  transitions: WorkflowTransition[];
}

export interface OverlayLevel {
  guid: string;
  title: string;
  hid?: string;
  fields: Record<string, unknown>;
}

export interface ResolvedEntry {
  guid: string;
  fields: Record<string, unknown>;
  fieldOrigin: Record<string, string>;
  chain: OverlayLevel[];
}

export interface LinkInfo {
  guid: string;
  type: string;
  source: string;
  target: string;
  sourceTitle: string;
  targetTitle: string;
  content: ArtifactContent | null;
}

export interface CommentInfo {
  guid: string;
  subject: string;
  parent: string;
  content: ArtifactContent | null;
}

export interface HistoryEntry {
  commit: string;
  message: string;
  author: string;
  when: string;
}

export interface ArtifactDetail extends ArtifactSummary {
  schema?: EffectiveSchema;
  workflows?: ArtifactWorkflow[];
  resolved?: ResolvedEntry;
  links?: LinkInfo[];
  comments?: CommentInfo[];
  hidHistory?: { hid: string; commit: string }[];
}

export interface FolderInfo { path: string; name: string }
export interface TypeCount { kind: string; type: string; count: number }

export interface TreeResponse {
  path: string;
  folders: FolderInfo[];
  artifacts: ArtifactSummary[];
  types: TypeCount[];
}

export interface StatusResponse {
  revision: string;
  gitHead: string;
  maintenance: boolean;
  reindex: { running: boolean; phase?: string; detail?: string };
  stats: Record<string, number | string>;
}

export interface PresenceEntry { user: string; viewing?: string; editing?: boolean }
