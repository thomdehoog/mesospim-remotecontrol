import { describe, it, expect } from 'vitest';
import { sanitizeHTML } from '../../src/sanitize';

describe('sanitizeHTML', () => {
  it('preserves ordinary formatting markup', () => {
    const html = '<p>Hello <b>bold</b> and <i>italic</i> with <a href="https://example.com">a link</a></p>';
    const out = sanitizeHTML(html);
    expect(out).toContain('<b>bold</b>');
    expect(out).toContain('<i>italic</i>');
    expect(out).toContain('href="https://example.com"');
  });

  it('strips inline event handlers', () => {
    const out = sanitizeHTML('<img src="x" onerror="window.__xss=1"> <div onclick="steal()">x</div>');
    expect(out).not.toMatch(/onerror/i);
    expect(out).not.toMatch(/onclick/i);
    expect(out).toContain('src="x"'); // the element itself is kept, only the handler removed
  });

  it('drops script-bearing and dangerous elements', () => {
    const out = sanitizeHTML('ok<script>window.__xss=1</script><iframe src="evil"></iframe><object></object>');
    expect(out).not.toMatch(/<script/i);
    expect(out).not.toMatch(/<iframe/i);
    expect(out).not.toMatch(/<object/i);
    expect(out).toContain('ok');
  });

  it('removes javascript: and other executable URL schemes', () => {
    const out = sanitizeHTML(
      '<a href="javascript:alert(1)">a</a><a href="vbscript:x">b</a><a href="JavaScript:alert(1)">c</a>');
    expect(out).not.toMatch(/javascript:/i);
    expect(out).not.toMatch(/vbscript:/i);
  });

  it('sees through control-character obfuscation of a scheme', () => {
    const out = sanitizeHTML('<a href="java\tscript:alert(1)">x</a>');
    expect(out.toLowerCase()).not.toContain('script:alert');
  });

  it('keeps data:image URLs but drops other data: URLs', () => {
    const img = sanitizeHTML('<img src="data:image/png;base64,AAAA">');
    expect(img).toContain('data:image/png');
    const bad = sanitizeHTML('<a href="data:text/html,<script>1</script>">x</a>');
    expect(bad).not.toContain('data:text/html');
  });

  it('is idempotent — sanitizing clean output changes nothing', () => {
    const once = sanitizeHTML('<p onclick="x">t <img src="y" onerror="z"></p>');
    expect(sanitizeHTML(once)).toBe(once);
  });

  it('handles empty and plain-text input', () => {
    expect(sanitizeHTML('')).toBe('');
    expect(sanitizeHTML('just text')).toBe('just text');
  });
});
