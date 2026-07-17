// Create dialog: new entries and documents. Types come from the schemas
// resolvable at the current repository location.

import { css, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { StoreElement } from './base';
import { api } from '../api';
import { errText, loadTree, notify, selectArtifact } from '../actions';
import { store } from '../store';

@customElement('origoa-create-dialog')
export class CreateDialog extends StoreElement {
  protected observe = ['schemas', 'path'] as never[];
  @state() private type = '';
  @state() private base = '';

  static styles = css`
    .backdrop {
      position: fixed; inset: 0; background: rgba(0,0,0,0.35);
      display: flex; align-items: center; justify-content: center; z-index: 10;
    }
    .dialog {
      background: var(--panel); border-radius: 12px; padding: 22px 26px;
      width: 440px; box-shadow: 0 10px 40px rgba(0,0,0,0.2);
    }
    h3 { margin: 0 0 14px; }
    label { display: block; margin: 10px 0 4px; color: var(--muted); font-size: 13px; }
    input, select {
      width: 100%; padding: 7px 10px; border: 1px solid var(--border);
      border-radius: 6px; font: inherit;
    }
    .row { display: flex; gap: 8px; justify-content: flex-end; margin-top: 18px; }
    button {
      border: 1px solid var(--border); background: var(--panel); border-radius: 6px;
      padding: 6px 16px; cursor: pointer; font: inherit;
    }
    button.primary { background: var(--accent); border-color: var(--accent); color: white; }
    .muted { color: var(--muted); font-size: 12px; margin-top: 4px; }
  `;

  render() {
    const s = this.app;
    const schema = s.schemas.find((x) => x.artifactType === this.type);
    return html`<div class="backdrop" @click=${(e: Event) => { if (e.target === e.currentTarget) this.close(); }}>
      <form class="dialog" @submit=${this.submit}>
        <h3>Create artifact</h3>
        <label>Location</label>
        <input id="folder" .value=${s.path} placeholder="folder path" />
        <label>Type</label>
        <select id="type" required @change=${(e: Event) => { this.type = (e.target as HTMLSelectElement).value; }}>
          <option value="" disabled ?selected=${!this.type}>Select a schema-defined type…</option>
          ${s.schemas.map((sc) => html`
            <option value=${sc.artifactType} ?selected=${this.type === sc.artifactType}>
              ${sc.displayName} (${sc.kind})
            </option>`)}
        </select>
        ${s.schemas.length === 0 ? html`<div class="muted">
          No schemas are defined at this location. Add schema files below a “.origoa/schemas” directory.
        </div>` : nothing}
        <label>Title</label>
        <input id="title" required placeholder="Artifact title" />
        ${schema?.kind === 'entry' ? html`
          <label>Overlay base (optional GUID or HID)</label>
          <input id="base" placeholder="Inherit fields from an existing entry"
            @input=${(e: Event) => { this.base = (e.target as HTMLInputElement).value; }} />
        ` : nothing}
        <div class="row">
          <button type="button" @click=${this.close}>Cancel</button>
          <button class="primary">Create</button>
        </div>
      </form>
    </div>`;
  }

  private close = () => store.update({ dialog: '' });

  private submit = async (e: Event) => {
    e.preventDefault();
    const folder = (this.renderRoot.querySelector('#folder') as HTMLInputElement).value.trim();
    const title = (this.renderRoot.querySelector('#title') as HTMLInputElement).value.trim();
    const schema = this.app.schemas.find((x) => x.artifactType === this.type);
    if (!schema) return;
    try {
      let base = this.base.trim();
      if (base && !/^[0-9a-f-]{36}$/.test(base)) {
        const res = await api.search({ fields: { hid: base } });
        if (res.results.length !== 1) throw new Error(`cannot resolve "${base}" to an entry`);
        base = res.results[0].guid;
      }
      const created = schema.kind === 'document'
        ? await api.createDocument({ folder, type: this.type, title })
        : await api.createEntry({ folder, type: this.type, title, base: base || undefined });
      notify(`${schema.displayName} created (${created.hid || created.guid.slice(0, 8)})`);
      this.close();
      store.update({ path: folder });
      loadTree();
      selectArtifact(created.guid);
    } catch (err) {
      store.update({ error: errText(err) });
    }
  };
}
