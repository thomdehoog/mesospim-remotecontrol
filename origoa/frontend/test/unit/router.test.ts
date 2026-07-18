import { describe, it, expect } from 'vitest';
import { stateToURL, urlToState } from '../../src/router';
import type { AppState } from '../../src/store';

function state(partial: Partial<AppState>): AppState {
  return {
    path: '', selected: '', query: '', subtree: false, typeFilter: '', panel: '',
    expanded: false, folders: [], artifacts: [], types: [], schemas: [], detail: null,
    status: null, presence: [], maintenance: false, notice: '', error: '', loading: false,
    dialog: '', ...partial,
  };
}

describe('router URL <-> state mapping', () => {
  it('encodes only non-default navigation fields', () => {
    expect(stateToURL(state({}))).toBe('/');
    expect(stateToURL(state({ path: 'engineering' }))).toBe('/?path=engineering');
    expect(stateToURL(state({ selected: 'g1', subtree: true })))
      .toBe('/?sel=g1&subtree=1');
  });

  it('decodes query parameters into partial state', () => {
    const s = urlToState('?path=eng&sel=g1&q=boot&subtree=1&type=requirement&panel=fields&expanded=1');
    expect(s).toMatchObject({
      path: 'eng', selected: 'g1', query: 'boot', subtree: true,
      typeFilter: 'requirement', panel: 'fields', expanded: true,
    });
  });

  it('round-trips arbitrary navigable state', () => {
    for (const nav of [
      { path: 'a/b/c', selected: 'guid-123', query: 'x y', subtree: true, typeFilter: 't', panel: 'comments', expanded: true },
      { path: 'engineering/safety', query: 'emergency stop' },
      { selected: 'only-selection' },
      {},
    ]) {
      const url = stateToURL(state(nav));
      const search = url.includes('?') ? url.slice(url.indexOf('?')) : '';
      const back = urlToState(search);
      for (const [k, v] of Object.entries(nav)) {
        expect((back as Record<string, unknown>)[k]).toEqual(v);
      }
    }
  });

  it('treats an empty query as all-default state', () => {
    expect(urlToState('')).toMatchObject({
      path: '', selected: '', query: '', subtree: false, typeFilter: '', panel: '', expanded: false,
    });
  });
});
