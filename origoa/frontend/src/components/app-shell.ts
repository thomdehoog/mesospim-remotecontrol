// Application shell: sidebar + main content area, notification banners,
// maintenance indicator and presence display.

import { css, html, nothing } from 'lit';
import { customElement } from 'lit/decorators.js';
import { StoreElement } from './base';
import { store } from '../store';
import { api } from '../api';
import { notify, errText, refreshStatus } from '../actions';
import './nav-sidebar';
import './artifact-table';
import './artifact-detail';
import './document-editor';
import './create-dialog';

@customElement('origoa-app')
export class OrigoaApp extends StoreElement {
  protected observe = [] as never[];

  static styles = css`
    :host { display: flex; flex-direction: column; height: 100vh; }
    header {
      display: flex; align-items: center; gap: 12px;
      padding: 8px 16px; background: var(--panel);
      border-bottom: 1px solid var(--border);
    }
    header .logo { font-weight: 700; font-size: 16px; color: var(--accent); letter-spacing: 0.5px; }
    header .rev { color: var(--muted); font-size: 12px; }
    header .spacer { flex: 1; }
    header .presence { color: var(--muted); font-size: 12px; }
    header button {
      border: 1px solid var(--border); background: var(--panel);
      border-radius: 6px; padding: 4px 10px; cursor: pointer; font: inherit;
    }
    header button:hover { background: var(--accent-soft); }
    .banner { padding: 6px 16px; font-size: 13px; }
    .banner.maintenance { background: #fff3cd; border-bottom: 1px solid #e6d9a8; }
    .banner.notice { background: var(--accent-soft); border-bottom: 1px solid var(--border); }
    .banner.error { background: #fde8e8; border-bottom: 1px solid #f5c2c0; color: var(--danger); }
    .body { display: flex; flex: 1; min-height: 0; }
    origoa-sidebar { width: 280px; flex-shrink: 0; }
    main { flex: 1; display: flex; flex-direction: column; min-width: 0; min-height: 0; }
    .split { display: flex; flex-direction: column; flex: 1; min-height: 0; }
    origoa-table { flex-shrink: 0; max-height: 45%; overflow: auto; }
    .detail-area { flex: 1; overflow: auto; border-top: 1px solid var(--border); }
    .detail-area.expanded { max-height: none; }
    .empty { color: var(--muted); padding: 48px; text-align: center; }
  `;

  render() {
    const s = this.app;
    const detail = s.detail;
    const viewers = s.presence.filter((p) => p.viewing && p.viewing === s.selected).length;
    return html`
      <header>
        <span class="logo">ORIGOA</span>
        <span class="rev" title=${s.status?.gitHead ?? ''}>
          ${s.status ? `rev ${String(s.status.revision).slice(0, 8) || '(empty)'}` : ''}
        </span>
        <span class="spacer"></span>
        <span class="presence">
          ${s.presence.length} online${viewers > 1 ? ` · ${viewers} viewing this artifact` : ''}
        </span>
        <button @click=${this.newFolder} title="Create a folder">+ Folder</button>
        <button @click=${() => store.update({ dialog: 'create' })} title="Create an artifact">+ Artifact</button>
        <button @click=${this.reindex} title="Rebuild all derived data from Git">Reindex</button>
      </header>
      ${s.maintenance ? html`<div class="banner maintenance">
        Repository maintenance in progress — write operations are temporarily unavailable.
        ${s.status?.reindex?.running ? html`(${s.status.reindex.phase}: ${s.status.reindex.detail})` : nothing}
      </div>` : nothing}
      ${s.notice ? html`<div class="banner notice">${s.notice}</div>` : nothing}
      ${s.error ? html`<div class="banner error">${s.error}
        <button @click=${() => store.update({ error: '' })}>dismiss</button></div>` : nothing}
      <div class="body">
        <origoa-sidebar></origoa-sidebar>
        <main>
          ${s.expanded && detail
            ? html`<div class="detail-area expanded">${this.detailView()}</div>`
            : html`<div class="split">
                <origoa-table></origoa-table>
                <div class="detail-area">
                  ${detail ? this.detailView() : html`<div class="empty">Select an artifact above, or create one.</div>`}
                </div>
              </div>`}
        </main>
      </div>
      ${s.dialog === 'create' ? html`<origoa-create-dialog></origoa-create-dialog>` : nothing}
    `;
  }

  private detailView() {
    const detail = this.app.detail!;
    return detail.kind === 'document'
      ? html`<origoa-document-editor></origoa-document-editor>`
      : html`<origoa-detail></origoa-detail>`;
  }

  private async newFolder() {
    const name = prompt('New folder path (relative to repository root):', this.app.path ? this.app.path + '/' : '');
    if (!name) return;
    try {
      await api.createFolder(name);
      notify(`Folder ${name} created`);
      store.update({ path: name.replace(/^\/+|\/+$/g, '') });
      const { loadTree } = await import('../actions');
      loadTree();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  }

  private async reindex() {
    if (!confirm('Rebuild all derived data from Git? The repository switches to read-only during reindexing.')) return;
    try {
      await api.reindex();
      notify('Reindex started');
      refreshStatus();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  }
}
