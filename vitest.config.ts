import { defineConfig } from 'vitest/config';

// Bazel creates a workspace-root symlink named bazel-<workspace>. Vitest's
// default discovery follows it, so an explicit file run can otherwise execute
// the same component test twice (once from the checkout and once through the
// Bazel execroot). Keep source-level smoke commands deterministic.
export default defineConfig({
  test: {
    exclude: ['**/node_modules/**', '**/bazel-*/**'],
  },
});
