const number = new Intl.NumberFormat('zh-TW', { maximumFractionDigits: 1 })

// Callers validate that the byte count is a nonnegative safe integer.
export function formatBytes(value: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let amount = value, unit = 0
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit++ }
  return `${number.format(amount)} ${units[unit]}`
}
