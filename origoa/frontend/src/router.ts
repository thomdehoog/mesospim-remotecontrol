// URL router — bidirectional synchronization between the application
// store and the browser URL via the History API. Every navigable state
// can be bookmarked or shared; opening the URL reconstructs the state.

import { store, navKeys, type AppState } from './store';

// stateToURL and urlToState are pure and inverse: they define the mapping
// between navigable application state and the browser URL. Kept exported and
// side-effect-free so the round trip can be unit tested directly.
export function stateToURL(s: AppState): string {
  const qs = new URLSearchParams();
  if (s.path) qs.set('path', s.path);
  if (s.selected) qs.set('sel', s.selected);
  if (s.query) qs.set('q', s.query);
  if (s.subtree) qs.set('subtree', '1');
  if (s.typeFilter) qs.set('type', s.typeFilter);
  if (s.panel) qs.set('panel', s.panel);
  if (s.expanded) qs.set('expanded', '1');
  const str = qs.toString();
  return str ? `/?${str}` : '/';
}

export function urlToState(search: string = location.search): Partial<AppState> {
  const qs = new URLSearchParams(search);
  return {
    path: qs.get('path') ?? '',
    selected: qs.get('sel') ?? '',
    query: qs.get('q') ?? '',
    subtree: qs.get('subtree') === '1',
    typeFilter: qs.get('type') ?? '',
    panel: qs.get('panel') ?? '',
    expanded: qs.get('expanded') === '1',
  };
}

let applyingURL = false;

export function initRouter(): void {
  // URL → store on load and on back/forward navigation.
  const apply = () => {
    applyingURL = true;
    store.update(urlToState(location.search));
    applyingURL = false;
  };
  window.addEventListener('popstate', apply);
  apply();

  // store → URL on navigation-relevant changes.
  store.subscribe((state, changed) => {
    if (applyingURL) return;
    if (!navKeys.some((k) => changed.has(k))) return;
    const url = stateToURL(state);
    if (url !== location.pathname + location.search) {
      history.pushState(null, '', url);
    }
  });
}
