// Artifact overview: a table of the artifacts matching the current
// navigation context. Beyond the fixed identity/status/social columns, the
// field columns are derived from the effective schemas of the types on
// display — the indexed fields the schema defines — rather than hard-coded,
// so the same generic table adapts to any application domain.

import { css, html } from 'lit';
import { customElement } from 'lit/decorators.js';
import { StoreElement } from './base';
import { selectArtifact } from '../actions';
import type { ArtifactSummary, FieldDef } from '../types';

// maxFieldColumns bounds how many schema-defined columns are shown so a
// type with many indexed fields cannot produce an unreadably wide table.
const maxFieldColumns = 4;

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
    .field { color: var(--text); font-size: 12px; }
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
    const fieldCols = this.fieldColumns();
    return html`
      <div class="caption">${caption} — ${s.artifacts.length} artifact(s)</div>
      ${s.artifacts.length === 0
        ? html`<div class="empty">No artifacts here.</div>`
        : html`<table>
            <thead>
              <tr>
                <th>Name</th><th>ID</th><th>Type</th>
                ${fieldCols.map((f) => html`<th>${f.displayName ?? f.id}</th>`)}
                <th>Status</th><th>Location</th><th>Social</th>
              </tr>
            </thead>
            <tbody>
              ${s.artifacts.map((a) => this.row(a, fieldCols))}
            </tbody>
          </table>`}
    `;
  }

  // fieldColumns derives the dynamic columns from the effective schemas of
  // the artifact types currently on display: the indexed fields those
  // schemas define, de-duplicated by id in first-seen order and capped.
  private fieldColumns(): FieldDef[] {
    const shownTypes = new Set(this.app.artifacts.map((a) => a.type));
    const out: FieldDef[] = [];
    const seen = new Set<string>();
    for (const schema of this.app.schemas) {
      if (!shownTypes.has(schema.artifactType)) continue;
      for (const f of schema.fields ?? []) {
        if (!f.indexed || seen.has(f.id)) continue;
        seen.add(f.id);
        out.push(f);
        if (out.length >= maxFieldColumns) return out;
      }
    }
    return out;
  }

  private row(a: ArtifactSummary, fieldCols: FieldDef[]) {
    const workflows = a.content?.workflows ?? {};
    const fields = a.content?.fields ?? {};
    const active = this.app.selected === a.guid;
    return html`<tr class="row ${active ? 'active' : ''}" @click=${() => selectArtifact(a.guid)}>
      <td>${a.title}</td>
      <td class="hid">${a.hid || a.guid.slice(0, 8)}</td>
      <td><span class="kind">${a.kind === 'document' ? '📄 ' : ''}${this.typeName(a.type)}</span></td>
      ${fieldCols.map((f) => html`<td class="field">${formatValue(fields[f.id])}</td>`)}
      <td>${Object.entries(workflows).map(([wf, st]) => html`<span class="state" title=${wf}>${st}</span>`)}</td>
      <td class="hid">/${a.parentPath}</td>
      <td class="social">🔗 ${a.linkCount} 💬 ${a.commentCount}</td>
    </tr>`;
  }

  private typeName(type: string): string {
    return this.app.schemas.find((sc) => sc.artifactType === type)?.displayName ?? type;
  }
}

// formatValue renders an indexed field value compactly for a table cell.
function formatValue(v: unknown): string {
  if (v === undefined || v === null || v === '') return '—';
  if (Array.isArray(v)) return v.map((x) => formatValue(x)).join(', ');
  if (typeof v === 'boolean') return v ? '✓' : '—';
  return String(v);
}
