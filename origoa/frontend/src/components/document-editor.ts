// Document editor: a WYSIWYG-style editor for hierarchical documents.
// The view focuses exclusively on the document content; document
// properties, relationships, comments and version information live in
// optional sidebars that can be enabled per task.

import { css, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { StoreElement } from './base';
import { api } from '../api';
import { errText, notify, refreshDetail, loadTree, selectArtifact } from '../actions';
import { store } from '../store';
import type { DocBlock, DocSection } from '../types';
import './panels';

interface RefInfo { title: string; hid: string; type: string }

@customElement('origoa-document-editor')
export class DocumentEditor extends StoreElement {
  protected observe = ['detail', 'expanded'] as never[];

  @state() private sections: DocSection[] = [];
  @state() private dirty = false;
  @state() private sidebar = ''; // '', 'properties', 'relationships', 'comments', 'versions'
  @state() private refs = new Map<string, RefInfo>();
  private loadedFor = '';

  static styles = css`
    :host { display: flex; height: 100%; }
    .paper-wrap { flex: 1; overflow-y: auto; padding: 24px; }
    .paper {
      max-width: 800px; margin: 0 auto; background: var(--panel);
      border: 1px solid var(--border); border-radius: 10px; padding: 48px 56px;
      min-height: 70vh; box-shadow: 0 1px 3px rgba(0,0,0,0.06);
    }
    .doc-title { font-size: 26px; font-weight: 700; margin-bottom: 18px; }
    .toolbar { display: flex; gap: 8px; margin-bottom: 14px; align-items: center; flex-wrap: wrap; }
    .toolbar .spacer { flex: 1; }
    button {
      border: 1px solid var(--border); background: var(--panel); border-radius: 6px;
      padding: 4px 10px; cursor: pointer; font: inherit; font-size: 13px;
    }
    button:hover { background: var(--accent-soft); }
    button.primary { background: var(--accent); border-color: var(--accent); color: white; }
    button.on { background: var(--accent-soft); border-color: var(--accent); color: var(--accent); }
    .section { margin: 6px 0; }
    .heading { display: flex; gap: 6px; align-items: center; }
    .heading input {
      flex: 1; border: 1px solid transparent; border-radius: 6px; padding: 2px 6px;
      font-weight: 700; font-family: inherit; background: transparent;
    }
    .heading input.h1 { font-size: 20px; }
    .heading input.h2 { font-size: 17px; }
    .heading input.h3 { font-size: 15px; }
    .heading input:hover, .heading input:focus { border-color: var(--border); background: var(--panel); }
    .heading .tools { visibility: hidden; display: flex; gap: 4px; }
    .section:hover > .heading .tools { visibility: visible; }
    .blocks { margin-left: 4px; }
    .block { position: relative; margin: 4px 0; }
    .block .btools { position: absolute; right: 0; top: 0; visibility: hidden; }
    .block:hover .btools { visibility: visible; }
    .text {
      border: 1px solid transparent; border-radius: 6px; padding: 4px 8px; min-height: 24px;
      line-height: 1.55;
    }
    .text:hover { border-color: var(--border); }
    .text:focus { border-color: var(--accent); outline: none; background: var(--panel); }
    .entry-ref {
      display: inline-flex; align-items: center; gap: 8px;
      background: var(--accent-soft); border: 1px solid var(--accent);
      border-radius: 8px; padding: 6px 12px; cursor: pointer; margin: 2px 0;
    }
    .entry-ref .hid { font-family: ui-monospace, monospace; font-size: 12px; color: var(--accent); }
    .entry-ref .etype { font-size: 11px; color: var(--muted); }
    img { max-width: 100%; border-radius: 6px; }
    .children { margin-left: 26px; border-left: 2px solid var(--bg); padding-left: 10px; }
    .add-row { display: flex; gap: 6px; margin: 6px 0; visibility: hidden; }
    .section:hover > .add-row, .paper > .add-row { visibility: visible; }
    aside {
      width: 300px; flex-shrink: 0; border-left: 1px solid var(--border);
      background: var(--panel); overflow-y: auto; padding: 14px;
    }
    aside h3 { margin: 0 0 10px; font-size: 14px; }
    .muted { color: var(--muted); font-size: 12px; }
    .meta { display: grid; grid-template-columns: 90px 1fr; gap: 4px 8px; font-size: 13px; }
    .meta .k { color: var(--muted); }
    .mono { font-family: ui-monospace, monospace; font-size: 12px; }
  `;

  render() {
    const d = this.app.detail;
    if (!d) return nothing;
    if (this.loadedFor !== d.guid + d.updatedCommit && !this.dirty) {
      this.sections = structuredClone(d.content?.sections ?? []);
      this.loadedFor = d.guid + d.updatedCommit;
      this.resolveRefs();
    }
    return html`
      <div class="paper-wrap">
        <div class="toolbar">
          <button class="primary" ?disabled=${!this.dirty} @click=${this.save}>
            ${this.dirty ? 'Save document' : 'Saved'}
          </button>
          ${this.dirty ? html`<button @click=${this.discard}>Discard</button>` : nothing}
          <span class="spacer"></span>
          ${(['properties', 'relationships', 'comments', 'versions'] as const).map((p) => html`
            <button class=${this.sidebar === p ? 'on' : ''}
              @click=${() => { this.sidebar = this.sidebar === p ? '' : p; }}>${p}</button>`)}
        </div>
        <div class="paper">
          <div class="doc-title">${d.title}</div>
          ${this.renderSections(this.sections, 1)}
          <div class="add-row">
            <button @click=${() => this.addSection(this.sections)}>+ Section</button>
          </div>
        </div>
      </div>
      ${this.sidebar ? this.renderSidebar() : nothing}
    `;
  }

  private renderSidebar() {
    const d = this.app.detail!;
    switch (this.sidebar) {
      case 'properties':
        return html`<aside>
          <h3>Document properties</h3>
          <div class="meta">
            <span class="k">GUID</span><span class="mono">${d.guid}</span>
            <span class="k">HID</span><span>${d.hid || '—'}</span>
            <span class="k">Type</span><span>${d.schema?.displayName ?? d.type}</span>
            <span class="k">Location</span><span class="mono">/${d.parentPath}</span>
            <span class="k">Revision</span><span class="mono">${d.updatedCommit.slice(0, 8)}</span>
          </div>
          <h3 style="margin-top:16px">Workflows</h3>
          <origoa-workflow-panel .detail=${d}></origoa-workflow-panel>
        </aside>`;
      case 'relationships':
        return html`<aside><h3>Relationships</h3>
          <origoa-links-panel .detail=${d}></origoa-links-panel></aside>`;
      case 'comments':
        return html`<aside><h3>Comments</h3>
          <origoa-comments-panel .detail=${d}></origoa-comments-panel></aside>`;
      case 'versions':
        return html`<aside><h3>Version information</h3>
          <origoa-history-panel .guid=${d.guid}></origoa-history-panel></aside>`;
    }
    return nothing;
  }

  private renderSections(sections: DocSection[], level: number): unknown {
    return sections.map((sec) => html`
      <div class="section">
        <div class="heading">
          <input class="h${Math.min(level, 3)}" placeholder="Section heading"
            .value=${sec.heading ?? ''}
            @input=${(e: Event) => { sec.heading = (e.target as HTMLInputElement).value; this.touch(); }} />
          <span class="tools">
            <button title="Add text block" @click=${() => this.addBlock(sec, 'text')}>¶</button>
            <button title="Insert entry reference" @click=${() => this.addEntryRef(sec)}>⧉</button>
            <button title="Add image" @click=${() => this.addImage(sec)}>🖼</button>
            <button title="Add subsection" @click=${() => this.addSection(sec.children ??= [])}>+§</button>
            <button title="Remove section" @click=${() => this.removeSection(sections, sec)}>✕</button>
          </span>
        </div>
        <div class="blocks">
          ${(sec.blocks ?? []).map((b) => this.renderBlock(sec, b))}
        </div>
        ${sec.children?.length ? html`<div class="children">${this.renderSections(sec.children, level + 1)}</div>` : nothing}
      </div>
    `);
  }

  private renderBlock(sec: DocSection, b: DocBlock) {
    const tools = html`<span class="btools">
      <button title="Remove block" @click=${() => this.removeBlock(sec, b)}>✕</button>
    </span>`;
    switch (b.type) {
      case 'entryRef': {
        const info = this.refs.get(b.guid ?? '');
        return html`<div class="block">
          <span class="entry-ref" @click=${() => b.guid && selectArtifact(b.guid)}
            title="Open referenced entry">
            <span class="hid">${info?.hid || (b.guid ?? '').slice(0, 8)}</span>
            <span>${info?.title ?? 'referenced entry'}</span>
            <span class="etype">${info?.type ?? ''}</span>
          </span>
          ${tools}
        </div>`;
      }
      case 'image':
        return html`<div class="block">
          <img src=${b.attachment ?? ''} alt="document image" />
          ${tools}
        </div>`;
      default:
        return html`<div class="block">
          <div class="text" contenteditable="true" .innerHTML=${b.text ?? ''}
            @input=${(e: Event) => { b.text = (e.target as HTMLElement).innerHTML; this.touch(); }}></div>
          ${tools}
        </div>`;
    }
  }

  private touch(): void {
    this.dirty = true;
    this.requestUpdate();
  }

  private addSection(target: DocSection[]): void {
    target.push({ id: crypto.randomUUID(), heading: '', blocks: [{ type: 'text', text: '' }] });
    this.touch();
  }

  private removeSection(list: DocSection[], sec: DocSection): void {
    if (!confirm('Remove this section including its content?')) return;
    const i = list.indexOf(sec);
    if (i >= 0) list.splice(i, 1);
    this.touch();
  }

  private addBlock(sec: DocSection, type: DocBlock['type']): void {
    (sec.blocks ??= []).push({ type, text: '' });
    this.touch();
  }

  private removeBlock(sec: DocSection, b: DocBlock): void {
    const i = (sec.blocks ?? []).indexOf(b);
    if (i >= 0) sec.blocks!.splice(i, 1);
    this.touch();
  }

  private async addEntryRef(sec: DocSection): Promise<void> {
    const q = prompt('Reference an entry by HID or GUID:');
    if (!q) return;
    try {
      let guid = q.trim();
      if (!/^[0-9a-f-]{36}$/.test(guid)) {
        const res = await api.search({ fields: { hid: guid } });
        if (res.results.length !== 1) throw new Error(`cannot resolve "${q}" to an entry`);
        guid = res.results[0].guid;
      }
      (sec.blocks ??= []).push({ type: 'entryRef', guid });
      this.touch();
      this.resolveRefs();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  }

  private addImage(sec: DocSection): void {
    const url = prompt('Image URL:');
    if (!url) return;
    (sec.blocks ??= []).push({ type: 'image', attachment: url });
    this.touch();
  }

  // Resolve entry references to their current titles for display.
  private async resolveRefs(): Promise<void> {
    const guids = new Set<string>();
    const walk = (secs: DocSection[]) => secs.forEach((s) => {
      s.blocks?.forEach((b) => { if (b.type === 'entryRef' && b.guid) guids.add(b.guid); });
      if (s.children) walk(s.children);
    });
    walk(this.sections);
    const refs = new Map(this.refs);
    await Promise.all([...guids].filter((g) => !refs.has(g)).map(async (g) => {
      try {
        const a = await api.artifact(g);
        refs.set(g, { title: a.title, hid: a.hid, type: a.type });
      } catch {
        refs.set(g, { title: '(deleted entry)', hid: g.slice(0, 8), type: '' });
      }
    }));
    this.refs = refs;
  }

  private save = async () => {
    const d = this.app.detail!;
    try {
      await api.updateArtifact(d.guid, { sections: this.sections, ifRevision: d.updatedCommit });
      this.dirty = false;
      notify('Document saved');
      refreshDetail();
      loadTree();
    } catch (err) {
      store.update({ error: errText(err) });
    }
  };

  private discard = () => {
    const d = this.app.detail!;
    this.sections = structuredClone(d.content?.sections ?? []);
    this.dirty = false;
  };
}
