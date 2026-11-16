import { describe, expect, it } from 'vitest'
import { needsTextLiteral, quoteExactText } from './text-literal'

const formatControls = String.fromCodePoint(0x061c, 0x200b, 0x202e, 0x2066, 0xfeff)
const separators = String.fromCodePoint(0x2028, 0x2029)
const directional = String.fromCodePoint(0x202e)
const astralFormat = String.fromCodePoint(0xe0001)

describe('exact text literals', () => {
  it.each(['', '00123', 'é 😀', '\0\t\r\n', '\u007f\u0085\u009f', formatControls, separators, astralFormat, '\ud800'])
  ('round-trips %j without invisible controls in the displayed literal', (value) => {
    const quoted = quoteExactText(value)
    expect(JSON.parse(quoted)).toBe(value)
    for (const character of quoted) {
      const code = character.codePointAt(0)!
      expect(code < 0x20 || code >= 0x7f && code <= 0x9f || code === 0x2028 || code === 0x2029 || /\p{Cf}/u.test(character)).toBe(false)
    }
  })
  it('selects literal entry for controls and format marks, not ordinary Unicode or leading zeros', () => {
    for (const value of ['\0', '\r', '\n', '\u007f', '\u0085', directional, separators, astralFormat]) expect(needsTextLiteral(value)).toBe(true)
    for (const value of ['', '00123', 'é', '😀']) expect(needsTextLiteral(value)).toBe(false)
  })
  it('keeps ordinary JSON spelling and explicitly escapes directional controls', () => {
    expect(quoteExactText('  00123 😀  ')).toBe(JSON.stringify('  00123 😀  '))
    expect(quoteExactText(`a${directional}b\u0085c`)).toBe('"a\\u202eb\\u0085c"')
    expect(quoteExactText(astralFormat)).toBe('"\\udb40\\udc01"')
  })
})
