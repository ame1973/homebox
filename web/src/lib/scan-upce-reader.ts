import BarcodeFormat from '@zxing/library/esm/core/BarcodeFormat'
import BitArray from '@zxing/library/esm/core/common/BitArray'
import NotFoundException from '@zxing/library/esm/core/NotFoundException'
import OneDReader from '@zxing/library/esm/core/oned/OneDReader'
import AbstractUPCEANReader from '@zxing/library/esm/core/oned/AbstractUPCEANReader'
import UPCEReader from '@zxing/library/esm/core/oned/UPCEReader'
import Result from '@zxing/library/esm/core/Result'
import ResultPoint from '@zxing/library/esm/core/ResultPoint'

// The UPC-E row decoding follows ZXing's Apache-2.0 algorithm; its license is
// distributed in public/licenses/zxing-library-0.23.0.txt. Version 0.23.0's
// experimental reader returns the wrong decodeMiddle shape and its parent uses
// EAN end/checksum rules. Reuse ZXing's row scanning, guard and digit readers,
// but retain UPC-E's own parity and checksum rules.
const patterns = [
  ...AbstractUPCEANReader.L_PATTERNS,
  ...AbstractUPCEANReader.L_PATTERNS.map((pattern) => Int32Array.from(pattern).reverse()),
]

function checksumInput(value: string): string {
  const body = value.slice(1, 7), end = body[5]
  if (end === '0' || end === '1' || end === '2') return value[0] + body.slice(0, 2) + end + '0000' + body.slice(2, 5) + value[7]
  if (end === '3') return value[0] + body.slice(0, 3) + '00000' + body.slice(3, 5) + value[7]
  if (end === '4') return value[0] + body.slice(0, 4) + '00000' + body[4] + value[7]
  return value[0] + body.slice(0, 5) + '0000' + end + value[7]
}

export class UPCEBarcodeReader extends OneDReader {
  decodeRow(rowNumber: number, row: BitArray): Result {
    const start = AbstractUPCEANReader.findStartGuardPattern(row)
    let offset = start[1]!, digits = '', parity = 0
    const counters = new Int32Array(4)
    for (let index = 0; index < 6; index++) {
      const match = AbstractUPCEANReader.decodeDigit(row, counters, offset, patterns)
      digits += String(match % 10)
      offset += counters.reduce((total, value) => total + value, 0)
      if (match >= 10) parity |= 1 << (5 - index)
    }
    const end = AbstractUPCEANReader.findGuardPatternWithoutCounters(row, offset, true, AbstractUPCEANReader.END_PATTERN)
    const quietEnd = end[1]! + end[1]! - end[0]!
    if (quietEnd >= row.getSize() || !row.isRange(end[1]!, quietEnd, false)) throw new NotFoundException()
    let code = ''
    for (let system = 0; system < 2; system++) {
      for (let check = 0; check < 10; check++) {
        const expected = UPCEReader.NUMSYS_AND_CHECK_DIGIT_PATTERNS[0]![check]! ^ (system === 1 ? 0x3f : 0)
        if (parity === expected) code = `${system}${digits}${check}`
      }
    }
    if (!code || !AbstractUPCEANReader.checkStandardUPCEANChecksum(checksumInput(code))) throw new NotFoundException()
    return new Result(code, new Uint8Array(), 0, [new ResultPoint((start[0]! + start[1]!) / 2, rowNumber), new ResultPoint((end[0]! + end[1]!) / 2, rowNumber)], BarcodeFormat.UPC_E)
  }
}
