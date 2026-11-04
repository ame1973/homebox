import { defineConfig, mergeConfig } from 'vitest/config'
import vite from './vite.config.ts'

export default mergeConfig(vite, defineConfig({
  test: {
    environment: 'jsdom',
    environmentOptions: { jsdom: { url: 'http://localhost/' } },
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['tests/setup.ts'],
    restoreMocks: true,
    coverage: {
      provider: 'v8', reporter: ['text', 'lcov'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.d.ts', 'src/**/*.test.*', 'src/components/ui/**', 'src/lib/utils.ts', 'src/main.tsx'],
      thresholds: { lines: 90, statements: 90, functions: 90, branches: 85 },
    },
  },
}))
