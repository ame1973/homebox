import { readFileSync } from 'node:fs'
import { test, expect } from './auth-fixture.ts'

// Compare the served production build with the exact installed dependency
// notices, not merely with another copy of the same generated file.
test('serves the bundled dependency notices without exposing arbitrary text', async ({ page, application }) => {
  const licenses = [
    ['zxing-library-0.23.0.txt', '../../node_modules/@zxing/library/LICENSE'],
    ['zxing-text-encoding-0.9.0.txt', '../../node_modules/@zxing/text-encoding/LICENSE.md'],
    ['ts-custom-error-3.3.1.txt', '../../node_modules/ts-custom-error/LICENSE'],
  ] as const
  for (const [name, source] of licenses) {
    const expected = readFileSync(new URL(source, import.meta.url))
    expect(readFileSync(new URL(`../../public/licenses/${name}`, import.meta.url))).toEqual(expected)
    const response = await page.request.get(`${application.url}/licenses/${name}`)
    expect(response.status()).toBe(200)
    expect(response.headers()['cache-control']).toBe('no-cache')
    expect(response.headers()['content-type']).toMatch(/^text\/plain/)
    expect(response.headers()['x-content-type-options']).toBe('nosniff')
    expect(await response.body()).toEqual(expected)
  }
  for (const path of ['/licenses/', '/licenses/private.txt', '/assets/private.txt', '/data/private.txt']) {
    const response = await page.request.get(`${application.url}${path}`)
    expect(response.status()).toBe(404)
    expect(response.headers()['cache-control']).toBe('no-store')
  }
})
