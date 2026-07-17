// Actions: user interactions and data loading update the store; the
// components re-render from store state.

import { api, ApiError } from './api';
import { store } from './store';

let treeSeq = 0;

export async function loadTree(): Promise<void> {
  const { path, subtree, query, typeFilter } = store.get();
  const seq = ++treeSeq;
  try {
    if (query) {
      const [tree, res, schemas] = await Promise.all([
        api.tree(path, subtree),
        api.search({ q: query, path, subtree: true }),
        api.schemas(path),
      ]);
      if (seq !== treeSeq) return;
      store.update({
        folders: tree.folders ?? [],
        types: tree.types ?? [],
        artifacts: (res.results ?? []).filter((a) => a.kind === 'entry' || a.kind === 'document'),
        schemas: schemas.types ?? [],
        error: '',
      });
      return;
    }
    const effectiveSubtree = subtree || !!typeFilter;
    const [tree, schemas] = await Promise.all([
      api.tree(path, effectiveSubtree),
      api.schemas(path),
    ]);
    if (seq !== treeSeq) return;
    let artifacts = tree.artifacts ?? [];
    if (typeFilter) artifacts = artifacts.filter((a) => a.type === typeFilter);
    store.update({
      folders: tree.folders ?? [],
      artifacts,
      types: tree.types ?? [],
      schemas: schemas.types ?? [],
      error: '',
    });
  } catch (err) {
    if (seq !== treeSeq) return;
    store.update({ error: errText(err) });
  }
}

export async function refreshDetail(): Promise<void> {
  const guid = store.get().selected;
  if (!guid) {
    store.update({ detail: null });
    return;
  }
  try {
    const detail = await api.artifact(guid);
    if (store.get().selected === guid) store.update({ detail, error: '' });
  } catch (err) {
    if (err instanceof ApiError && err.status === 410) {
      store.update({ detail: null, notice: 'This artifact was deleted.' });
      return;
    }
    store.update({ error: errText(err) });
  }
}

export async function refreshStatus(): Promise<void> {
  try {
    const status = await api.status();
    store.update({ status, maintenance: status.maintenance });
  } catch { /* backend unreachable: keep last status */ }
}

export function navigateFolder(path: string): void {
  store.update({ path, selected: '', typeFilter: '', query: '' });
  loadTree();
}

export function selectArtifact(guid: string): void {
  store.update({ selected: guid, panel: '' });
  refreshDetail();
}

export function setQuery(q: string): void {
  store.update({ query: q, selected: '' });
  loadTree();
}

export function setSubtree(on: boolean): void {
  store.update({ subtree: on });
  loadTree();
}

export function setTypeFilter(type: string): void {
  store.update({ typeFilter: type, selected: '' });
  loadTree();
}

export function notify(text: string): void {
  store.update({ notice: text });
  window.setTimeout(() => {
    if (store.get().notice === text) store.update({ notice: '' });
  }, 4000);
}

export function errText(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}
