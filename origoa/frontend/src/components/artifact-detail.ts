// Entry detail view: the complete artifact on a single scrollable page,
// generated from its effective schema — general information, fields
// (with overlay resolution), workflows, relationships, comments and
// history — with quick links to the individual sections.

import { css, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { StoreElement } from './base';
import { api } from '../api';
import { errText, notify, refreshDetail, loadTree } from '../actions';
import { store } from '../store';
import './field-editor';
import './panels';

@customElement('origoa-detail')
export class ArtifactDetailView extends StoreElement {
  protected observe = ['detail', 'panel', 'expanded', 'presence'] as never[];

  // Pending (unsaved) field edits.
  @state() private pending: Record<string, unknown> = {};
  @state() private pendingTitle: string | null = null;
  private pendingFor = '';

  static styles = css`
    :host { display: block; padding: 16px 24px 60px; }
    .head { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
    .head input.title {
      font-size: 20px; font-weight: 700; border: 1px solid transparent;
      border-radius: 6px; padding: 2px 6px; flex: 1; min-width: 240px; font-family: inherit;
      background: transparent;
    }
    .head input.title:hover, .head input.title:focus { border-color: var(--border); background: var(--panel); }
    .hid { font-family: ui-monospace, monospace; color: var(--muted); }
    .quick { display: flex; gap: 10px; margin: 10px 0 16px; flex-wrap: wrap; font-size: 13px; }
    .quick a { color: var(--accent); cursor: pointer; text-decoration: none; }
    .quick a:hover { text-decoration: underline; }
    section {
      background: var(--panel); border: 1px solid var(--border); border-radius: 10px;
      padding: 14px 18px; margin-bottom: 14px;
    }
    section h3 { margin: 0 0 10px; font-size: 14px; }
    .meta { display: grid; grid-template-columns: 140px 1fr; gap: 4px 12px; font-size: 13px; }
    .meta .k { color: var(--muted); }
    .mono { font-family: ui-monospace, monospace; font-size: 12px; }
    .actions { position: sticky; bottom: 0; display: flex; gap: 8px; padding: 10px 0; }
    button {
      border: 1px solid var(--border); background: var(--panel); border-radius: 6px;
      padding: 6px 14px; cursor: pointer; font: inherit;
    }
    button:hover { background: var(--accent-soft); }
    button.primary { background: var(--accent); border-color: var(--accent); color: white; }
    button.danger { color: var(--danger); }
    .viewers { font-size: 12px; color: var(--accent); background: var(--accent-soft);
      padding: 2px 8px; border-radius: 10px; }
  `;

  render() {
    const d = this.app.detail;
    if (!d) return nothing;
    if (this.pendingFor !== d.guid + d.updatedCommit) {
      // A different artifact (or revision) was loaded: drop stale edits.
      this.pending = {};
      this.pendingTitle = null;
      this.pendingFor = d.guid + d.updatedCommit;
    }
    const schema = d.schema;
    const resolved = d.resolved;
    const dirty = Object.keys(this.pending).length > 0 || this.pendingTitle !== null;
    this.publishEditing(dirty);
    const others = this.app.presence.filter((p) => p.viewing === d.guid).length - 1;
    return html`
      <div class="head">
        <input class="title" .value=${this.pendingTitle ?? d.title}
          @input=${(e: Event) => { this.pendingTitle = (e.target as HTMLInputElement).value; }} />
        <span class="hid" title="Human-readable identifier">${d.hid}</span>
        ${others > 0 ? html`<span class="viewers">👁 ${others} other user(s) viewing</span>` : nothing}
        <button @click=${() => store.update({ expanded: !this.app.expanded })}>
          ${this.app.expanded ? 'Split view' : 'Expand'}
        </button>
      </div>
      <div class="quick">
        ${['general', 'fields', 'workflows', 'relationships', 'comments', 'overlay', 'history'].map((sec) => html`
          <a @click=${() => this.jump(sec)}>${sec}</a>`)}
      </div>

      <section id="general">
        <h3>General</h3>
        <div class="meta">
          <span class="k">GUID</span><span class="mono">${d.guid}</span>
          <span class="k">Type</span><span>${schema?.displayName ?? d.type} <span class="mono">(${d.type})</span></span>
          <span class="k">Location</span><span class="mono">/${d.parentPath}</span>
          <span class="k">Revision</span><span class="mono">${d.updatedCommit.slice(0, 8)}</span>
          ${(d.hidHistory?.length ?? 0) > 1 ? html`
            <span class="k">Former IDs</span>
            <span>${d.hidHistory!.slice(0, -1).map((h) => h.hid).join(', ')}</span>` : nothing}
          ${d.content?.base ? html`<span class="k">Overlay base</span><span class="mono">${d.content.base}</span>` : nothing}
        </div>
      </section>

      <section id="fields">
        <h3>Fields</h3>
        ${!schema ? html`<div class="mono">No schema defines type “${d.type}” at this location.</div>` : nothing}
        <div @field-change=${this.onFieldChange}>
          ${(schema?.fields ?? []).map((f) => {
            const effective = this.pending[f.id] !== undefined
              ? this.pending[f.id]
              : resolved
                ? resolved.fields[f.id]
                : d.content?.fields?.[f.id];
            const inherited = resolved !== undefined && this.pending[f.id] === undefined &&
              resolved.fieldOrigin[f.id] !== undefined && resolved.fieldOrigin[f.id] !== d.guid;
            return html`<origoa-field .def=${f} .value=${effective ?? null}
              .enums=${schema?.enums ?? {}} .inherited=${inherited}></origoa-field>`;
          })}
        </div>
      </section>

      <section id="workflows">
        <h3>Workflows</h3>
        <origoa-workflow-panel .detail=${d}></origoa-workflow-panel>
      </section>

      <section id="relationships">
        <h3>Relationships</h3>
        <origoa-links-panel .detail=${d}></origoa-links-panel>
      </section>

      <section id="comments">
        <h3>Comments</h3>
        <origoa-comments-panel .detail=${d}></origoa-comments-panel>
      </section>

      <section id="overlay">
        <h3>Overlay composition</h3>
        <origoa-overlay-panel .detail=${d}></origoa-overlay-panel>
      </section>

      <section id="history">
        <h3>History</h3>
        <origoa-history-panel .guid=${d.guid + ''}></origoa-history-panel>
      </section>

      <div class="actions">
        <button class="primary" ?disabled=${!dirty} @click=${this.save}>
          ${dirty ? 'Save changes' : 'Saved'}
        </button>
        ${dirty ? html`<button @click=${() => { this.pending = {}; this.pendingTitle = null; }}>Discard</button>` : nothing}
        <span style="flex:1"></span>
        <button @click=${this.move}>Move…</button>
        <button class="danger" @click=${this.removeArtifact}>Delete</button>
      </div>
    `;
  }

  // publishEditing mirrors this view's unsaved-edit state into the store so
  // the session client can broadcast editing presence and so an incoming
  // remote change to the same artifact preserves (rather than discards) the
  // local edits. This element does not observe 'editing', so the update
  // never re-enters its own render.
  private publishEditing(dirty: boolean): void {
    if (this.app.editing !== dirty) store.update({ editing: dirty });
  }

  disconnectedCallback(): void {
    super.disconnectedCallback();
    if (this.app.editing) store.update({ editing: false });
  }

  private jump(id: string): void {
    store.update({ panel: id });
    this.renderRoot.querySelector(`#${id}`)?.scrollIntoView({ behavior: 'smooth' });
  }

  private onFieldChange = (e: Event) => {
    const { id, value } = (e as CustomEvent).detail;
    this.pending = { ...this.pending, [id]: value };
  };

  private save = async () => {
    const d = this.app.detail!;
    const patch: Record<string, unknown> = { ifRevision: d.updatedCommit };
    if (Object.keys(this.pending).length) patch.fields = this.pending;
    if (this.pendingTitle !== null && this.pendingTitle !== d.title) patch.title = this.pendingTitle;
    try {
      await api.updateArtifact(d.guid, patch);
      this.pending = {};
      this.pendingTitle = null;
      notify('Saved');
      refreshDetail();
      loadTree();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  };

  private move = async () => {
    const d = this.app.detail!;
    const folder = prompt('Move to folder:', d.parentPath);
    if (folder === null) return;
    try {
      await api.moveArtifact(d.guid, folder);
      notify(`Moved to /${folder}`);
      refreshDetail();
      loadTree();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  };

  private removeArtifact = async () => {
    const d = this.app.detail!;
    if (!confirm(`Delete "${d.title}"? The artifact remains recoverable from Git history.`)) return;
    try {
      await api.deleteArtifact(d.guid, d.updatedCommit);
      notify('Artifact deleted');
      store.update({ selected: '', detail: null });
      loadTree();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  };
}
