// Central application store.
//
// All persistent frontend state lives here. Components never talk to
// each other directly: they observe the store and update themselves when
// the relevant state changes; user interactions update the store, which
// propagates the new state to every interested component.

import type {
  ArtifactDetail, ArtifactSummary, EffectiveSchema, FolderInfo,
  PresenceEntry, StatusResponse, TypeCount,
} from './types';

export interface AppState {
  // Navigation (synchronized with the browser URL)
  path: string;            // selected folder
  selected: string;        // selected artifact GUID ('' = none)
  query: string;           // active search query
  subtree: boolean;        // include nested folders
  typeFilter: string;      // filter artifact list by type ('' = all)
  panel: string;           // active detail section anchor
  expanded: boolean;       // detail view occupies the full workspace

  // Data
  folders: FolderInfo[];
  artifacts: ArtifactSummary[];
  types: TypeCount[];
  schemas: EffectiveSchema[];
  detail: ArtifactDetail | null;
  status: StatusResponse | null;

  // Transient session state
  presence: PresenceEntry[];
  maintenance: boolean;
  notice: string;          // transient notification banner
  error: string;
  loading: boolean;
  dialog: string;          // active modal dialog ('' = none)
}

const initial: AppState = {
  path: '',
  selected: '',
  query: '',
  subtree: false,
  typeFilter: '',
  panel: '',
  expanded: false,
  folders: [],
  artifacts: [],
  types: [],
  schemas: [],
  detail: null,
  status: null,
  presence: [],
  maintenance: false,
  notice: '',
  error: '',
  loading: false,
  dialog: '',
};

type Listener = (state: AppState, changed: Set<keyof AppState>) => void;

// Store is exported (not just its singleton) so tests can exercise isolated
// instances without sharing global state.
export class Store {
  private state: AppState = { ...initial };
  private listeners = new Set<Listener>();

  get(): AppState {
    return this.state;
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  update(partial: Partial<AppState>): void {
    const changed = new Set<keyof AppState>();
    for (const key of Object.keys(partial) as (keyof AppState)[]) {
      if (this.state[key] !== partial[key]) changed.add(key);
    }
    if (changed.size === 0) return;
    this.state = { ...this.state, ...partial };
    for (const fn of this.listeners) fn(this.state, changed);
  }
}

export const store = new Store();

// navKeys are the state properties reflected into the browser URL.
export const navKeys: (keyof AppState)[] = [
  'path', 'selected', 'query', 'subtree', 'typeFilter', 'panel', 'expanded',
];
