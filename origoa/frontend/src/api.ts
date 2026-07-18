// REST client — the authoritative interface for persistent repository
// data. All CRUD and service calls go through here; the WebSocket is
// reserved for transient session information.

import type {
  ArtifactDetail, ArtifactSummary, CommentInfo, EffectiveSchema,
  HistoryEntry, LinkInfo, ResolvedEntry, StatusResponse, TreeResponse,
  WorkflowDef,
} from './types';

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data: unknown = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON error body */ }
  if (!res.ok) {
    const msg = (data as { error?: string } | null)?.error ?? `HTTP ${res.status}`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const api = {
  status: () => request<StatusResponse>('GET', '/api/status'),
  reindex: () => request<{ started: boolean }>('POST', '/api/reindex'),

  tree: (path: string, subtree: boolean) =>
    request<TreeResponse>('GET', `/api/tree?path=${encodeURIComponent(path)}&subtree=${subtree}`),

  search: (params: { q?: string; kind?: string; type?: string; path?: string; subtree?: boolean; fields?: Record<string, string> }) => {
    const qs = new URLSearchParams();
    if (params.q) qs.set('q', params.q);
    if (params.kind) qs.set('kind', params.kind);
    if (params.type) qs.set('type', params.type);
    if (params.path) qs.set('path', params.path);
    if (params.subtree) qs.set('subtree', 'true');
    for (const [k, v] of Object.entries(params.fields ?? {})) qs.set(`field.${k}`, v);
    return request<{ results: ArtifactSummary[] }>('GET', `/api/search?${qs}`);
  },

  schemas: (path: string) =>
    request<{ types: EffectiveSchema[] }>('GET', `/api/schemas?path=${encodeURIComponent(path)}`),
  effectiveSchema: (path: string, type: string) =>
    request<EffectiveSchema>('GET', `/api/schemas/effective?path=${encodeURIComponent(path)}&type=${encodeURIComponent(type)}`),
  workflowDef: (name: string, path: string) =>
    request<WorkflowDef>('GET', `/api/workflows/${encodeURIComponent(name)}?path=${encodeURIComponent(path)}`),

  createEntry: (p: { folder: string; type: string; title: string; hid?: string; base?: string; fields?: Record<string, unknown> }) =>
    request<ArtifactSummary>('POST', '/api/entries', p),
  createDocument: (p: { folder: string; type: string; title: string; fields?: Record<string, unknown> }) =>
    request<ArtifactSummary>('POST', '/api/documents', p),

  artifact: (guid: string) => request<ArtifactDetail>('GET', `/api/artifacts/${guid}`),
  updateArtifact: (guid: string, patch: Record<string, unknown>) =>
    request<ArtifactSummary>('PATCH', `/api/artifacts/${guid}`, patch),
  deleteArtifact: (guid: string, ifRevision?: string) =>
    request<{ deleted: string }>('DELETE', `/api/artifacts/${guid}${ifRevision ? `?ifRevision=${ifRevision}` : ''}`),
  moveArtifact: (guid: string, folder: string) =>
    request<ArtifactSummary>('POST', `/api/artifacts/${guid}/move`, { folder }),

  links: (guid: string) => request<{ links: LinkInfo[] }>('GET', `/api/artifacts/${guid}/links`),
  comments: (guid: string) => request<{ comments: CommentInfo[] }>('GET', `/api/artifacts/${guid}/comments`),
  history: (guid: string) => request<{ history: HistoryEntry[] }>('GET', `/api/artifacts/${guid}/history`),
  overlay: (guid: string) => request<ResolvedEntry>('GET', `/api/artifacts/${guid}/overlay`),

  transition: (guid: string, workflow: string, to: string, ifRevision?: string) =>
    request<ArtifactSummary>('POST', `/api/artifacts/${guid}/workflows/${encodeURIComponent(workflow)}/transition`, { to, ifRevision }),

  createLink: (p: { type: string; source: string; target: string }) =>
    request<{ guid: string }>('POST', '/api/links', p),
  updateLink: (guid: string, patch: { fields?: Record<string, unknown>; ifRevision?: string }) =>
    request<ArtifactSummary>('PATCH', `/api/links/${guid}`, patch),
  createComment: (p: { subject: string; parent?: string; author?: string; text: string }) =>
    request<{ guid: string }>('POST', '/api/comments', p),
  updateComment: (guid: string, patch: { text?: string; ifRevision?: string }) =>
    request<ArtifactSummary>('PATCH', `/api/comments/${guid}`, patch),

  createFolder: (path: string) => request<{ path: string }>('POST', '/api/folders', { path }),
};
