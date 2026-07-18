import { defineConfig } from 'vitest/config';

// Unit tests for pure frontend logic. jsdom supplies the DOM the sanitizer
// needs (template parsing, TreeWalker) without launching a browser.
export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['test/unit/**/*.test.ts'],
  },
});
