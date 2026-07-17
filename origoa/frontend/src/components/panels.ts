// Shared detail panels: workflow interaction, relationships, comments,
// overlay composition and history. Used by both the entry detail view
// and the document editor sidebar.

import { css, html, svg, LitElement, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { api } from '../api';
import { errText, notify, refreshDetail, loadTree, selectArtifact } from '../actions';
import { store } from '../store';
import type {
  ArtifactDetail, ArtifactWorkflow, CommentInfo, LinkInfo, WorkflowDef,
} from '../types';

const panelStyles = css`
  :host { display: block; }
  h4 { margin: 0 0 8px; font-size: 13px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.5px; }
  button {
    border: 1px solid var(--border); background: var(--panel); border-radius: 6px;
    padding: 4px 10px; cursor: pointer; font: inherit; font-size: 13px;
  }
  button:hover { background: var(--accent-soft); }
  button.primary { background: var(--accent); border-color: var(--accent); color: white; }
  select, input, textarea {
    padding: 5px 8px; border: 1px solid var(--border); border-radius: 6px; font: inherit; font-size: 13px;
  }
  .muted { color: var(--muted); font-size: 12px; }
  .mono { font-family: ui-monospace, monospace; font-size: 12px; }
  .item { padding: 6px 0; border-bottom: 1px solid var(--bg); }
  a.ref { color: var(--accent); cursor: pointer; text-decoration: none; }
  a.ref:hover { text-decoration: underline; }
`;

// ---- Workflow panel ----

@customElement('origoa-workflow-panel')
export class WorkflowPanel extends LitElement {
  @property({ attribute: false }) detail!: ArtifactDetail;
  @state() private diagramFor = '';

  static styles = [panelStyles, css`
    .wf { display: flex; align-items: center; gap: 8px; padding: 6px 0; flex-wrap: wrap; }
    .wf .name { font-weight: 600; min-width: 90px; }
    .state { padding: 2px 10px; border-radius: 10px; background: var(--accent-soft); color: var(--accent); }
    svg { background: var(--bg); border-radius: 8px; margin-top: 6px; }
  `];

  render() {
    const wfs = this.detail.workflows ?? [];
    if (wfs.length === 0) return html`<div class="muted">No workflows assigned by the schema.</div>`;
    return html`${wfs.map((wf) => this.renderWorkflow(wf))}`;
  }

  private renderWorkflow(wf: ArtifactWorkflow) {
    const def = wf.definition;
    const transitions = wf.transitions ?? [];
    return html`
      <div class="wf">
        <span class="name">${def.displayName || def.name}</span>
        <span class="state">${this.stateName(def, wf.state)}</span>
        ${transitions.length ? html`
          <select id="sel-${def.name}">
            ${transitions.map((t) => html`<option value=${t.to}>${t.name || `→ ${t.to}`}</option>`)}
          </select>
          <button class="primary" @click=${() => this.transition(def.name)}>Apply</button>
        ` : html`<span class="muted">final state</span>`}
        <button title="Show workflow diagram"
          @click=${() => { this.diagramFor = this.diagramFor === def.name ? '' : def.name; }}>?</button>
      </div>
      ${this.diagramFor === def.name ? this.diagram(def, wf.state) : nothing}
    `;
  }

  private stateName(def: WorkflowDef, id: string): string {
    return def.states.find((s) => s.id === id)?.displayName || id;
  }

  // A minimal workflow diagram: states on a horizontal band, transitions
  // as arcs. Rendering aid for orientation, not an editor.
  private diagram(def: WorkflowDef, current: string) {
    const w = 150, gap = 30, h = 130;
    const width = def.states.length * (w + gap) + gap;
    const x = (i: number) => gap + i * (w + gap);
    const idx = new Map(def.states.map((s, i) => [s.id, i]));
    return html`<svg width=${Math.min(width, 900)} height=${h} viewBox="0 0 ${width} ${h}">
      <defs>
        <marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto">
          <path d="M0,0 L10,5 L0,10 z" fill="#6b7480"></path>
        </marker>
      </defs>
      ${def.transitions.map((t) => {
        const a = idx.get(t.from), b = idx.get(t.to);
        if (a === undefined || b === undefined) return nothing;
        const x1 = x(a) + w / 2, x2 = x(b) + w / 2;
        const up = a > b;
        const y = up ? 30 : 100;
        const cy = up ? 8 : 122;
        return svg`
          <path d="M ${x1} ${y} C ${x1} ${cy}, ${x2} ${cy}, ${x2} ${y}"
            fill="none" stroke="#6b7480" marker-end="url(#arr)"></path>
          <text x=${(x1 + x2) / 2} y=${up ? 16 : 120} text-anchor="middle" font-size="10" fill="#6b7480">${t.name ?? ''}</text>`;
      })}
      ${def.states.map((s0, i) => svg`
        <rect x=${x(i)} y="42" width=${w} height="34" rx="8"
          fill=${s0.id === current ? '#2563eb' : '#ffffff'} stroke="#dde1e6"></rect>
        <text x=${x(i) + w / 2} y="63" text-anchor="middle" font-size="12"
          fill=${s0.id === current ? '#ffffff' : '#1b1f24'}>${s0.displayName || s0.id}</text>`)}
    </svg>`;
  }

  private async transition(workflow: string): Promise<void> {
    const sel = this.renderRoot.querySelector<HTMLSelectElement>(`#sel-${CSS.escape(workflow)}`);
    if (!sel?.value) return;
    try {
      await api.transition(this.detail.guid, workflow, sel.value, this.detail.updatedCommit);
      notify(`Transitioned ${workflow} → ${sel.value}`);
      refreshDetail();
      loadTree();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  }
}

// ---- Relationships panel ----

@customElement('origoa-links-panel')
export class LinksPanel extends LitElement {
  @property({ attribute: false }) detail!: ArtifactDetail;
  @state() private adding = false;

  static styles = [panelStyles, css`
    .dir { color: var(--muted); width: 24px; display: inline-block; text-align: center; }
    .type { background: var(--bg); border: 1px solid var(--border); border-radius: 10px;
      padding: 1px 8px; font-size: 11px; color: var(--muted); margin-right: 6px; }
    form { display: flex; gap: 6px; margin-top: 8px; flex-wrap: wrap; }
    input { flex: 1; min-width: 120px; }
  `];

  render() {
    const links = this.detail.links ?? [];
    const guid = this.detail.guid;
    const rels = this.detail.schema?.relationships ?? [];
    return html`
      ${links.length === 0 ? html`<div class="muted">No relationships yet.</div>` : nothing}
      ${links.map((l) => this.renderLink(l, guid))}
      ${this.adding ? html`
        <form @submit=${this.create}>
          <input id="ltype" list="reltypes" placeholder="link type" required />
          <datalist id="reltypes">${rels.map((r) => html`<option value=${r.linkType}></option>`)}</datalist>
          <input id="ltarget" placeholder="target GUID or HID" required />
          <button class="primary">Create link</button>
          <button type="button" @click=${() => { this.adding = false; }}>Cancel</button>
        </form>` : html`<button @click=${() => { this.adding = true; }}>+ Add relationship</button>`}
    `;
  }

  private renderLink(l: LinkInfo, guid: string) {
    const outgoing = l.source === guid;
    const otherGuid = outgoing ? l.target : l.source;
    const otherTitle = outgoing ? l.targetTitle : l.sourceTitle;
    return html`<div class="item">
      <span class="dir" title=${outgoing ? 'outgoing' : 'incoming'}>${outgoing ? '→' : '←'}</span>
      <span class="type">${l.type}</span>
      <a class="ref" @click=${() => selectArtifact(otherGuid)}>${otherTitle || otherGuid}</a>
      <button style="float:right" title="Delete link" @click=${() => this.removeLink(l.guid)}>✕</button>
    </div>`;
  }

  private create = async (e: Event) => {
    e.preventDefault();
    const type = this.renderRoot.querySelector<HTMLInputElement>('#ltype')!.value.trim();
    let target = this.renderRoot.querySelector<HTMLInputElement>('#ltarget')!.value.trim();
    try {
      if (!/^[0-9a-f-]{36}$/.test(target)) {
        // Resolve an HID to its GUID via search.
        const res = await api.search({ fields: { hid: target } });
        if (res.results.length !== 1) throw new Error(`cannot resolve "${target}" to an artifact`);
        target = res.results[0].guid;
      }
      await api.createLink({ type, source: this.detail.guid, target });
      notify('Link created');
      this.adding = false;
      refreshDetail();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  };

  private async removeLink(guid: string): Promise<void> {
    if (!confirm('Delete this link?')) return;
    try {
      await api.deleteArtifact(guid);
      refreshDetail();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  }
}

// ---- Comments panel ----

@customElement('origoa-comments-panel')
export class CommentsPanel extends LitElement {
  @property({ attribute: false }) detail!: ArtifactDetail;
  @state() private replyTo = '';

  static styles = [panelStyles, css`
    .comment { padding: 8px 10px; background: var(--bg); border-radius: 8px; margin-bottom: 6px; }
    .comment .head { display: flex; gap: 8px; font-size: 12px; color: var(--muted); margin-bottom: 4px; }
    .thread { margin-left: 22px; }
    form { display: flex; gap: 6px; margin-top: 8px; }
    input.text { flex: 1; }
  `];

  render() {
    const comments = this.detail.comments ?? [];
    const roots = comments.filter((c) => !c.parent);
    return html`
      ${comments.length === 0 ? html`<div class="muted">No comments yet.</div>` : nothing}
      ${roots.map((c) => this.renderThread(c, comments))}
      ${this.form('')}
    `;
  }

  private renderThread(c: CommentInfo, all: CommentInfo[]): unknown {
    const children = all.filter((x) => x.parent === c.guid);
    return html`
      <div class="comment">
        <div class="head">
          <b>${c.content?.author || 'anonymous'}</b>
          <span>${c.content?.createdAt ? new Date(c.content.createdAt).toLocaleString() : ''}</span>
          <a class="ref" style="margin-left:auto" @click=${() => { this.replyTo = this.replyTo === c.guid ? '' : c.guid; }}>reply</a>
        </div>
        <div>${c.content?.text ?? ''}</div>
        ${this.replyTo === c.guid ? this.form(c.guid) : nothing}
      </div>
      ${children.length ? html`<div class="thread">${children.map((k) => this.renderThread(k, all))}</div>` : nothing}
    `;
  }

  private form(parent: string) {
    return html`<form @submit=${(e: Event) => this.submit(e, parent)}>
      <input class="text" placeholder=${parent ? 'Reply…' : 'Add a comment…'} required />
      <button class="primary">${parent ? 'Reply' : 'Comment'}</button>
    </form>`;
  }

  private async submit(e: Event, parent: string): Promise<void> {
    e.preventDefault();
    const input = (e.target as HTMLFormElement).querySelector<HTMLInputElement>('input.text')!;
    try {
      await api.createComment({
        subject: this.detail.guid,
        parent: parent || undefined,
        text: input.value,
        author: 'web-user',
      });
      input.value = '';
      this.replyTo = '';
      refreshDetail();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  }
}

// ---- Overlay composition panel ----

@customElement('origoa-overlay-panel')
export class OverlayPanel extends LitElement {
  @property({ attribute: false }) detail!: ArtifactDetail;

  static styles = [panelStyles, css`
    .level { border: 1px solid var(--border); border-radius: 8px; padding: 8px 10px; margin-bottom: 4px; background: var(--panel); }
    .level.self { border-color: var(--accent); }
    .arrow { text-align: center; color: var(--muted); }
    .fields { margin-top: 4px; font-size: 12px; color: var(--muted); }
    .win { color: var(--ok); font-weight: 600; }
  `];

  render() {
    const r = this.detail.resolved;
    if (!r || r.chain.length <= 1) {
      return html`<div class="muted">This entry has no overlay base. Set a base to create a variant.</div>`;
    }
    return html`${r.chain.map((level, i) => html`
      ${i > 0 ? html`<div class="arrow">▲ overlays</div>` : nothing}
      <div class="level ${i === 0 ? 'self' : ''}">
        <a class="ref" @click=${() => selectArtifact(level.guid)}>
          <b>${level.title}</b> ${level.hid ? html`<span class="mono">(${level.hid})</span>` : nothing}
        </a>
        <div class="fields">
          ${Object.keys(level.fields).length === 0 ? 'no own fields' : Object.entries(level.fields).map(([k]) => html`
            <span class=${r.fieldOrigin[k] === level.guid ? 'win' : ''}
              title=${r.fieldOrigin[k] === level.guid ? 'effective value' : 'overridden by a more specific level'}>${k}</span> `)}
        </div>
      </div>`)}`;
  }
}

// ---- History panel ----

@customElement('origoa-history-panel')
export class HistoryPanel extends LitElement {
  @property({ attribute: false }) guid = '';
  @state() private entries: { commit: string; message: string; author: string; when: string }[] = [];
  @state() private loaded = false;

  static styles = [panelStyles];

  updated(changed: Map<string, unknown>): void {
    if (changed.has('guid') && this.guid) {
      this.loaded = false;
      api.history(this.guid).then((r) => {
        this.entries = r.history ?? [];
        this.loaded = true;
      }).catch(() => { this.loaded = true; });
    }
  }

  render() {
    if (!this.loaded) return html`<div class="muted">Loading history…</div>`;
    if (this.entries.length === 0) return html`<div class="muted">No history recorded.</div>`;
    return html`${this.entries.map((e) => html`
      <div class="item">
        <div>${e.message}</div>
        <div class="muted"><span class="mono">${e.commit.slice(0, 8)}</span>
          — ${e.author}, ${new Date(e.when).toLocaleString()}</div>
      </div>`)}`;
  }
}
