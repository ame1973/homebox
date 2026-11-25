import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const publicFile = (name) => new URL(`../public/${name}`, import.meta.url)

test('the PWA manifest keeps a stable same-origin identity and launch scope', () => {
  const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
  assert.match(html, /<link\s+rel="manifest"\s+href="\/manifest\.webmanifest"\s*\/>/)
  assert.match(html, /<link\s+rel="apple-touch-icon"\s+href="\/icons\/homebox-180\.png"\s*\/>/)
  assert.match(html, /<meta\s+name="theme-color"\s+content="#28548c"\s*\/>/)

  const manifest = JSON.parse(readFileSync(publicFile('manifest.webmanifest'), 'utf8'))
  assert.equal(manifest.id, '/')
  assert.equal(manifest.start_url, '/')
  assert.equal(manifest.scope, '/')
  assert.equal(manifest.display, 'standalone')
  assert.equal(manifest.name, 'HomeBox 家庭物品管理')
  assert.equal(manifest.short_name, 'HomeBox')
  assert.equal(manifest.lang, 'zh-Hant')
  assert.equal(manifest.theme_color, '#28548c')
  assert.equal(manifest.background_color, '#f7f9fc')
  assert.deepEqual(manifest.icons, [
    { src: '/icons/homebox-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
    { src: '/icons/homebox-512.png', sizes: '512x512', type: 'image/png', purpose: 'any maskable' },
    { src: '/icons/homebox.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'any' },
  ])
})

test('the declared raster icons contain real PNGs at their stated dimensions', () => {
  for (const size of [180, 192, 512]) {
    const png = readFileSync(publicFile(`icons/homebox-${size}.png`))
    assert.ok(png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])))
    assert.equal(png.toString('ascii', 12, 16), 'IHDR')
    assert.equal(png.readUInt32BE(16), size)
    assert.equal(png.readUInt32BE(20), size)
    assert.equal(png.toString('ascii', png.length - 8, png.length - 4), 'IEND')
  }
  assert.match(readFileSync(publicFile('icons/homebox.svg'), 'utf8'), /<svg\s+xmlns="http:\/\/www\.w3\.org\/2000\/svg"/)
})
