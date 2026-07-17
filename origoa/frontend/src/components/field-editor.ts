// Schema-driven field editor: renders one artifact field according to
// its schema definition. The frontend contains no application-specific
// editors — everything derives from the effective schema.

import { css, html, LitElement, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import type { FieldDef } from '../types';

@customElement('origoa-field')
export class FieldEditor extends LitElement {
  @property({ attribute: false }) def!: FieldDef;
  @property({ attribute: false }) value: unknown;
  @property({ type: Boolean }) inherited = false;
  @property({ attribute: false }) enums: Record<string, { values: string[] }> = {};

  static styles = css`
    :host { display: contents; }
    .row { display: grid; grid-template-columns: 180px 1fr; gap: 10px; padding: 6px 0; align-items: start; }
    label { color: var(--muted); padding-top: 5px; }
    label .req { color: var(--danger); }
    input, select, textarea {
      width: 100%; padding: 5px 8px; border: 1px solid var(--border);
      border-radius: 6px; font: inherit; background: var(--panel);
    }
    textarea { min-height: 70px; resize: vertical; }
    .rich { border: 1px solid var(--border); border-radius: 6px; padding: 8px; min-height: 60px; background: var(--panel); }
    .rich:focus { outline: 2px solid var(--accent-soft); }
    .inherited { font-size: 11px; color: var(--accent); background: var(--accent-soft);
      border-radius: 8px; padding: 0 6px; margin-left: 6px; }
    .checks { display: flex; flex-wrap: wrap; gap: 10px; padding-top: 5px; }
  `;

  private emit(value: unknown): void {
    this.dispatchEvent(new CustomEvent('field-change', {
      detail: { id: this.def.id, value },
      bubbles: true, composed: true,
    }));
  }

  render() {
    const d = this.def;
    return html`<div class="row">
      <label>
        ${d.displayName || d.id}${d.required ? html`<span class="req">*</span>` : nothing}
        ${this.inherited ? html`<span class="inherited" title="Value inherited from the overlay base">inherited</span>` : nothing}
      </label>
      <div>${this.editor()}</div>
    </div>`;
  }

  private options(): string[] {
    const d = this.def;
    if (d.options?.length) return d.options;
    if (d.enum && this.enums[d.enum]) return this.enums[d.enum].values;
    return [];
  }

  private editor() {
    const d = this.def;
    const v = this.value;
    switch (d.type) {
      case 'boolean':
        return html`<input type="checkbox" .checked=${v === true}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).checked)} />`;
      case 'integer':
      case 'float':
      case 'currency':
        return html`<input type="number" step=${d.type === 'integer' ? '1' : 'any'}
          .value=${v == null ? '' : String(v)}
          @change=${(e: Event) => {
            const raw = (e.target as HTMLInputElement).value;
            this.emit(raw === '' ? null : Number(raw));
          }} />`;
      case 'date':
        return html`<input type="date" .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).value || null)} />`;
      case 'time':
        return html`<input type="time" .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).value || null)} />`;
      case 'datetime':
        return html`<input type="datetime-local" .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).value || null)} />`;
      case 'enum': {
        const opts = this.options();
        if (d.multiple) {
          const selected = Array.isArray(v) ? (v as string[]) : [];
          return html`<div class="checks">${opts.map((o) => html`
            <label><input type="checkbox" .checked=${selected.includes(o)}
              @change=${(e: Event) => {
                const on = (e.target as HTMLInputElement).checked;
                const next = on ? [...selected, o] : selected.filter((x) => x !== o);
                this.emit(next);
              }} /> ${o}</label>`)}</div>`;
        }
        return html`<select @change=${(e: Event) => this.emit((e.target as HTMLSelectElement).value || null)}>
          <option value="" ?selected=${!v}></option>
          ${opts.map((o) => html`<option value=${o} ?selected=${v === o}>${o}</option>`)}
        </select>`;
      }
      case 'multitext':
        return html`<textarea .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLTextAreaElement).value)}></textarea>`;
      case 'richtext':
        return html`<div class="rich" contenteditable="true" .innerHTML=${String(v ?? '')}
          @blur=${(e: Event) => this.emit((e.target as HTMLElement).innerHTML)}></div>`;
      case 'hyperlink':
        return html`<input type="url" placeholder="https://…" .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).value || null)} />
          ${v ? html`<a href=${String(v)} target="_blank" rel="noopener">open ↗</a>` : nothing}`;
      case 'artifact':
        return html`<input placeholder="artifact GUID or HID" .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).value || null)} />`;
      case 'artifacts':
        return html`<input placeholder="comma-separated GUIDs/HIDs"
          .value=${Array.isArray(v) ? (v as string[]).join(', ') : String(v ?? '')}
          @change=${(e: Event) => {
            const raw = (e.target as HTMLInputElement).value.trim();
            this.emit(raw ? raw.split(',').map((x) => x.trim()) : []);
          }} />`;
      case 'object':
      case 'attachment':
        return html`<textarea .value=${v == null ? '' : JSON.stringify(v, null, 2)}
          @change=${(e: Event) => {
            const raw = (e.target as HTMLTextAreaElement).value.trim();
            if (!raw) { this.emit(null); return; }
            try { this.emit(JSON.parse(raw)); } catch { /* keep previous value on invalid JSON */ }
          }}></textarea>`;
      default: // text, hid and unknown types
        return html`<input .value=${String(v ?? '')}
          @change=${(e: Event) => this.emit((e.target as HTMLInputElement).value)} />`;
    }
  }
}
