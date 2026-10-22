import { defineConfig } from '@playwright/test'
export default defineConfig({
  testDir: './tests', testMatch: ['ask-markdown.spec.ts', 'ask-widget.spec.ts'], workers: 1,
  reporter: 'list', use: { viewport: { width: 390, height: 700 } },
})
