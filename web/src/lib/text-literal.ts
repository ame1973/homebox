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
