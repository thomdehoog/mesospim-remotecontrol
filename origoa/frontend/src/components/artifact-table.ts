// Artifact overview: a table of all artifacts matching the current
// navigation context with name, type, workflow status and social
// indicators (links, comments).

import { css, html } from 'lit';
import { customElement } from 'lit/decorators.js';
import { StoreElement } from './base';
import { selectArtifact } from '../actions';
import type { ArtifactSummary } from '../types';

@customElement('origoa-table')
export class ArtifactTable extends StoreElement {
  protected observe = ['artifacts', 'selected', 'query', 'typeFilter', 'path', 'schemas'] as never[];

  static styles = css`
    :host { display: block; background: var(--panel); }
    table { width: 100%; border-collapse: collapse; font-size: 13px; }
    th {
      position: sticky; top: 0; background: var(--panel);
      text-align: left; padding: 8px 12px; color: var(--muted);
      font-weight: 600; border-bottom: 1px solid var(--border); font-size: 12px;
    }
    td { padding: 7px 12px; border-bottom: 1px solid var(--bg); }
    tr.row { cursor: pointer; }
    tr.row:hover td { background: var(--accent-soft); }
    tr.row.active td { background: var(--accent-soft); }
    .hid { color: var(--muted); font-family: ui-monospace, monospace; font-size: 12px; }
    .kind { display: inline-block; padding: 1px 7px; border-radius: 10px; font-size: 11px;
      background: var(--bg); border: 1px solid var(--border); color: var(--muted); }
    .state { display: inline-block; padding: 1px 7px; border-radius: 10px; font-size: 11px;
      background: var(--accent-soft); color: var(--accent); margin-right: 4px; }
    .social { color: var(--muted); font-size: 12px; white-space: nowrap; }
    .caption { padding: 6px 12px; color: var(--muted); font-size: 12px; border-bottom: 1px solid var(--border); }
    .empty { padding: 24px; color: var(--muted); text-align: center; }
  `;

  render() {
    const s = this.app;
    const caption = s.query
      ? `Search results for “${s.query}”`
      : s.typeFilter
        ? `All ${s.typeFilter} artifacts below /${s.path}`
        : `Contents of /${s.path || ''}`;
    return html`
      <div class="caption">${caption} — ${s.artifacts.length} artifact(s)</div>
      ${s.artifacts.length === 0
        ? html`<div class="empty">No artifacts here.</div>`
        : html`<table>
            <thead>
              <tr><th>Name</th><th>ID</th><th>Type</th><th>Status</th><th>Location</th><th>Social</th></tr>
            </thead>
            <tbody>
              ${s.artifacts.map((a) => this.row(a))}
            </tbody>
          </table>`}
    `;
  }

  private row(a: ArtifactSummary) {
    const workflows = a.content?.workflows ?? {};
    const active = this.app.selected === a.guid;
    return html`<tr class="row ${active ? 'active' : ''}" @click=${() => selectArtifact(a.guid)}>
      <td>${a.title}</td>
      <td class="hid">${a.hid || a.guid.slice(0, 8)}</td>
      <td><span class="kind">${a.kind === 'document' ? '📄 ' : ''}${this.typeName(a.type)}</span></td>
      <td>${Object.entries(workflows).map(([wf, st]) => html`<span class="state" title=${wf}>${st}</span>`)}</td>
      <td class="hid">/${a.parentPath}</td>
      <td class="social">🔗 ${a.linkCount} 💬 ${a.commentCount}</td>
    </tr>`;
  }

  private typeName(type: string): string {
    return this.app.schemas.find((sc) => sc.artifactType === type)?.displayName ?? type;
  }
}
