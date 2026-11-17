// Match JSON Schema/Go code-point lengths without normalization or silent lone
// UTF-16 surrogate repair. Callers still own semantic field validation.
export function validText(value: string, minimum: number, maximum: number): boolean {
  if (value.length > maximum * 2) return false
  let length = 0
  for (const character of value) {
    if (character.length === 1 && character.charCodeAt(0) >= 0xD800 && character.charCodeAt(0) <= 0xDFFF) return false
    length++
  }
  return length >= minimum && length <= maximum
}

// JSON.stringify escapes C0 controls but leaves C1, format controls and other
// default-ignorable code points invisible. Escape them as UTF-16 code units.
function hiddenCharacter(character: string): boolean {
  const code = character.codePointAt(0)!
  return code >= 0x7f && code <= 0x9f || code === 0x2028 || code === 0x2029 || /[\p{Cf}\p{Default_Ignorable_Code_Point}]/u.test(character)
}

export function needsTextLiteral(value: string): boolean {
  return [...value].some((character) => character.codePointAt(0)! < 0x20 || hiddenCharacter(character))
}

export function quoteExactText(value: string): string {
  let result = ''
  for (const character of JSON.stringify(value)) {
    if (!hiddenCharacter(character)) { result += character; continue }
    for (let index = 0; index < character.length; index++) result += `\\u${character.charCodeAt(index).toString(16).padStart(4, '0')}`
  }
  return result
}
