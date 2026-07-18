// Minimal HTML sanitizer for untrusted rich-text and document content.
//
// Rich text and document text blocks are stored as HTML and rendered via
// innerHTML, so content authored by one user is displayed to another. This
// strips the vectors that turn that into stored XSS — script-bearing elements,
// inline event handlers and dangerous URL schemes — using the browser's own
// parser rather than a regex, while preserving ordinary formatting markup.

const BLOCKED_TAGS = new Set([
  'SCRIPT', 'STYLE', 'IFRAME', 'OBJECT', 'EMBED', 'LINK', 'META', 'BASE', 'FORM',
]);

// Attributes whose value is a URL and must not carry an executable scheme.
const URL_ATTRS = new Set(['href', 'src', 'xlink:href', 'action', 'formaction']);

// Matches leading control/whitespace characters (U+0000–U+0020) that browsers
// ignore inside a URL scheme, e.g. "java\tscript:".
const IGNORED = /[\u0000-\u0020]+/g;

function isDangerousURL(value: string): boolean {
  const v = value.replace(IGNORED, '').toLowerCase();
  return v.startsWith('javascript:') || v.startsWith('vbscript:') ||
    (v.startsWith('data:') && !v.startsWith('data:image/'));
}

/**
 * sanitizeHTML returns a cleaned copy of untrusted HTML: blocked elements are
 * dropped, `on*` event-handler attributes are removed, and URL attributes with
 * an executable scheme are stripped. Text and safe formatting are preserved.
 */
export function sanitizeHTML(input: string): string {
  const tpl = document.createElement('template');
  tpl.innerHTML = input;
  const walker = document.createTreeWalker(tpl.content, NodeFilter.SHOW_ELEMENT);
  const toRemove: Element[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const el = node as Element;
    if (BLOCKED_TAGS.has(el.tagName)) {
      toRemove.push(el);
      continue;
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      if (name.startsWith('on')) {
        el.removeAttribute(attr.name);
      } else if (URL_ATTRS.has(name) && isDangerousURL(attr.value)) {
        el.removeAttribute(attr.name);
      }
    }
  }
  for (const el of toRemove) el.remove();
  return tpl.innerHTML;
}
