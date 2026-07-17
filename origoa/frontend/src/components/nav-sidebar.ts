// Repository navigation: search, subtree toggle, folder hierarchy and
// type-based exploration of entries and documents.

import { css, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { StoreElement } from './base';
import { navigateFolder, setQuery, setSubtree, setTypeFilter } from '../actions';
import { api } from '../api';
import type { FolderInfo } from '../types';

@customElement('origoa-sidebar')
export class Sidebar extends StoreElement {
  protected observe = ['path', 'folders', 'types', 'subtree', 'query', 'typeFilter', 'schemas'] as never[];

  // Folder tree expansion state and lazily loaded children.
  @state() private expandedFolders = new Set<string>();
  @state() private childFolders = new Map<string, FolderInfo[]>();

  static styles = css`
    :host {
      display: flex; flex-direction: column;
      background: var(--panel); border-right: 1px solid var(--border);
      overflow-y: auto; padding: 12px 8px; gap: 12px;
    }
    input[type='search'] {
      width: 100%; padding: 6px 10px; border: 1px solid var(--border);
      border-radius: 6px; font: inherit;
    }
    label.toggle { display: flex; align-items: center; gap: 6px; color: var(--muted); font-size: 13px; }
    h3 {
      margin: 8px 4px 4px; font-size: 11px; text-transform: uppercase;
      letter-spacing: 1px; color: var(--muted);
    }
    .node { display: flex; align-items: center; gap: 4px; padding: 3px 6px; border-radius: 6px; cursor: pointer; }
    .node:hover { background: var(--accent-soft); }
    .node.active { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
    .node .arrow { width: 14px; text-align: center; color: var(--muted); flex-shrink: 0; }
    .indent { margin-left: 16px; }
    .type-item { display: flex; justify-content: space-between; padding: 3px 6px 3px 24px; border-radius: 6px; cursor: pointer; }
    .type-item:hover { background: var(--accent-soft); }
    .type-item.active { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
    .count { color: var(--muted); font-size: 12px; }
  `;

  render() {
    const s = this.app;
    const entryTypes = s.types.filter((t) => t.kind === 'entry');
    const docTypes = s.types.filter((t) => t.kind === 'document');
    return html`
      <input type="search" placeholder="Search repository…"
        .value=${s.query}
        @change=${(e: Event) => setQuery((e.target as HTMLInputElement).value)} />
      <label class="toggle">
        <input type="checkbox" .checked=${s.subtree}
          @change=${(e: Event) => setSubtree((e.target as HTMLInputElement).checked)} />
        Include subtree
      </label>

      <div>
        <h3>Repository</h3>
        <div class="node ${s.path === '' && !s.typeFilter ? 'active' : ''}" @click=${() => navigateFolder('')}>
          <span class="arrow">▾</span><span>Root</span>
        </div>
        <div class="indent">${this.renderFolders(this.app.folders, '')}</div>
      </div>

      ${entryTypes.length ? html`<div>
        <h3>Entries</h3>
        ${entryTypes.map((t) => this.typeItem(t.type, t.count))}
      </div>` : nothing}

      ${docTypes.length ? html`<div>
        <h3>Documents</h3>
        ${docTypes.map((t) => this.typeItem(t.type, t.count))}
      </div>` : nothing}
    `;
  }

  private typeItem(type: string, count: number) {
    const active = this.app.typeFilter === type;
    return html`<div class="type-item ${active ? 'active' : ''}"
      @click=${() => setTypeFilter(active ? '' : type)}>
      <span>${this.displayName(type)}</span><span class="count">${count}</span>
    </div>`;
  }

  private displayName(type: string): string {
    return this.app.schemas.find((sc) => sc.artifactType === type)?.displayName ?? type;
  }

  private renderFolders(folders: FolderInfo[], parent: string): unknown {
    return folders.map((f) => {
      const expanded = this.expandedFolders.has(f.path);
      const kids = this.childFolders.get(f.path);
      return html`
        <div class="node ${this.app.path === f.path ? 'active' : ''}">
          <span class="arrow" @click=${(e: Event) => { e.stopPropagation(); this.toggle(f.path); }}>
            ${expanded ? '▾' : '▸'}
          </span>
          <span style="flex:1" @click=${() => navigateFolder(f.path)}>${f.name}</span>
        </div>
        ${expanded && kids ? html`<div class="indent">${this.renderFolders(kids, f.path)}</div>` : nothing}
      `;
    });
  }

  private async toggle(path: string): Promise<void> {
    const next = new Set(this.expandedFolders);
    if (next.has(path)) {
      next.delete(path);
    } else {
      next.add(path);
      if (!this.childFolders.has(path)) {
        const tree = await api.tree(path, false);
        const map = new Map(this.childFolders);
        map.set(path, tree.folders ?? []);
        this.childFolders = map;
      }
    }
    this.expandedFolders = next;
  }
}
