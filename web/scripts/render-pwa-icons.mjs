import { readFile, writeFile } from 'node:fs/promises'
import { chromium } from '@playwright/test'

if (process.argv.length !== 3 || process.argv[2] !== '--write') {
  throw new Error('Pass --write to replace the tracked PWA icon PNGs.')
}

const source = await readFile(new URL('../public/icons/homebox.svg', import.meta.url), 'utf8')
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  for (const size of [180, 192, 512]) {
    const encoded = await page.evaluate(async ({ svg, pixels }) => {
      const url = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }))
      try {
        const image = new Image()
        image.src = url
        await image.decode()
        const canvas = document.createElement('canvas')
        canvas.width = canvas.height = pixels
        const context = canvas.getContext('2d')
        if (!context) throw new Error('Canvas is unavailable')
        context.drawImage(image, 0, 0, pixels, pixels)
        return canvas.toDataURL('image/png').split(',')[1]
      } finally {
        URL.revokeObjectURL(url)
      }
    }, { svg: source, pixels: size })
    if (!encoded) throw new Error('Canvas did not produce a PNG')
    await writeFile(new URL(`../public/icons/homebox-${size}.png`, import.meta.url), Buffer.from(encoded, 'base64'))
  }
} finally {
  await browser.close()
}
