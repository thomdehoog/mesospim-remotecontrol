import { describe, it, expect } from 'vitest';
import { Store } from '../../src/store';

describe('Store', () => {
  it('starts from the initial state', () => {
    const s = new Store();
    expect(s.get().path).toBe('');
    expect(s.get().selected).toBe('');
    expect(s.get().subtree).toBe(false);
  });

  it('applies partial updates immutably', () => {
    const s = new Store();
    const before = s.get();
    s.update({ path: 'engineering', subtree: true });
    expect(s.get().path).toBe('engineering');
    expect(s.get().subtree).toBe(true);
    expect(s.get()).not.toBe(before); // new object, old snapshot untouched
    expect(before.path).toBe('');
  });

  it('notifies subscribers only about changed keys', () => {
    const s = new Store();
    const seen: Array<Set<string>> = [];
    s.subscribe((_state, changed) => seen.push(changed as Set<string>));
    s.update({ path: 'a', selected: 'g1' });
    expect(seen).toHaveLength(1);
    expect([...seen[0]].sort()).toEqual(['path', 'selected']);
  });

  it('is a no-op when values are unchanged (no notification)', () => {
    const s = new Store();
    s.update({ path: 'a' });
    let calls = 0;
    s.subscribe(() => calls++);
    s.update({ path: 'a' }); // same value
    expect(calls).toBe(0);
  });

  it('unsubscribes cleanly', () => {
    const s = new Store();
    let calls = 0;
    const off = s.subscribe(() => calls++);
    s.update({ path: 'x' });
    off();
    s.update({ path: 'y' });
    expect(calls).toBe(1);
  });
});
