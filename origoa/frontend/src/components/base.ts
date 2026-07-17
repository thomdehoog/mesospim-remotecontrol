// StoreElement: a LitElement that re-renders whenever observed store
// properties change. Components observe the store instead of talking to
// each other.

import { LitElement } from 'lit';
import { store, type AppState } from '../store';

export class StoreElement extends LitElement {
  /** Store properties that trigger a re-render; empty = all changes. */
  protected observe: (keyof AppState)[] = [];
  private unsubscribe?: () => void;

  protected get app(): AppState {
    return store.get();
  }

  connectedCallback(): void {
    super.connectedCallback();
    this.unsubscribe = store.subscribe((_state, changed) => {
      if (this.observe.length === 0 || this.observe.some((k) => changed.has(k))) {
        this.requestUpdate();
      }
    });
  }

  disconnectedCallback(): void {
    this.unsubscribe?.();
    super.disconnectedCallback();
  }
}
