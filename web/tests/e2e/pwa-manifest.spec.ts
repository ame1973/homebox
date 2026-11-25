import { readFileSync } from 'node:fs'
import { test, expect } from './auth-fixture.ts'

test('serves the same-origin manifest and actual icon pixels', async ({ page, application }) => {
  const shell = await page.goto(`${application.url}/`)
  expect(shell?.status()).toBe(200)
  expect(shell?.headers()['cache-control']).toBe('no-store')
  expect(await page.locator('link[rel="manifest"]').getAttribute('href')).toBe('/manifest.webmanifest')
  expect(await page.locator('link[rel="apple-touch-icon"]').getAttribute('href')).toBe('/icons/homebox-180.png')

  const cdp = await page.context().newCDPSession(page)
  try {
    const result = await cdp.send('Page.getAppManifest')
    expect(result.url).toBe(`${application.url}/manifest.webmanifest`)
    expect(result.errors).toEqual([])
    expect(result.manifest.id).toBe(`${application.url}/`)
    expect(result.manifest.startUrl).toBe(`${application.url}/`)
    expect(result.manifest.scope).toBe(`${application.url}/`)
    expect(result.manifest.name).toBe('HomeBox 家庭物品管理')
    expect(result.manifest.display).toBe('kStandalone')
    expect(JSON.parse(result.data ?? '{}').lang).toBe('zh-Hant')
  } finally {
    await cdp.detach()
  }

  for (const name of ['manifest.webmanifest', 'icons/homebox.svg', 'icons/homebox-180.png', 'icons/homebox-192.png', 'icons/homebox-512.png']) {
    const response = await page.request.get(`${application.url}/${name}`)
    expect(response.status(), name).toBe(200)
    expect(response.headers()['cache-control'], name).toBe('no-cache')
    expect(response.headers()['x-content-type-options'], name).toBe('nosniff')
    expect(await response.body(), name).toEqual(readFileSync(new URL(`../../public/${name}`, import.meta.url)))
    expect(response.headers()['content-type'], name).toMatch(name.endsWith('.png') ? /^image\/png/ : name.endsWith('.svg') ? /^image\/svg\+xml/ : /^application\/manifest\+json/)
  }

  const checks = await page.evaluate(async () => {
    const load = async (src: string) => {
      const image = new Image()
      image.src = src
      await image.decode()
      return image
    }
    const svg = await load('/icons/homebox.svg')
    const results = []
    for (const size of [180, 192, 512]) {
      const png = await load(`/icons/homebox-${size}.png`)
      const canvas = document.createElement('canvas')
      canvas.width = canvas.height = size
      const context = canvas.getContext('2d')!
      context.drawImage(png, 0, 0)
      const raster = context.getImageData(0, 0, size, size).data
      context.clearRect(0, 0, size, size)
      context.drawImage(svg, 0, 0, size, size)
      const vector = context.getImageData(0, 0, size, size).data
      let difference = 0
      let outsideSafeZone = 0
      let translucent = 0
      for (let offset = 0; offset < raster.length; offset += 4) {
        for (let component = 0; component < 4; component++) {
          difference = Math.max(difference, Math.abs(raster[offset + component]! - vector[offset + component]!))
        }
        if (raster[offset + 3] !== 255) translucent++
        const pixel = offset / 4
        const x = pixel % size + 0.5
        const y = Math.floor(pixel / size) + 0.5
        if (raster[offset]! > 220 && raster[offset + 1]! > 220 && raster[offset + 2]! > 220 &&
            Math.hypot(x - size / 2, y - size / 2) > size * 0.4) outsideSafeZone++
      }
      results.push({ size, width: png.naturalWidth, height: png.naturalHeight, difference, translucent, outsideSafeZone })
    }
    return results
  })
  for (const result of checks) {
    expect(result.width).toBe(result.size)
    expect(result.height).toBe(result.size)
    expect(result.difference).toBeLessThanOrEqual(1)
    expect(result.translucent).toBe(0)
    expect(result.outsideSafeZone).toBe(0)
  }
})
