import { parseExcelCell } from './structuredAnswerSources'

/** The original-coordinate sheet-view API uses `cell`, not `ref`. */
export type StructuredSheetCell = {
  row: number
  column: number
  cell?: string
  ref?: string
  display?: string | null
  value?: unknown
  formula?: string | null
}

export type StructuredSheetView = {
  sheet_name: string
  cells: StructuredSheetCell[]
  warnings?: string[]
  range: Record<string, number>
}

export type StructuredSheetDisplayCell = StructuredSheetCell & { ref: string }

export function sheetColumnLabel(column: number): string {
  if (!Number.isSafeInteger(column) || column < 1 || column > 16_384) throw new Error('原件窗口列坐标无效。')
  let value = column, label = ''
  while (value > 0) {
    const remainder = (value - 1) % 26
    label = String.fromCharCode(65 + remainder) + label
    value = Math.floor((value - 1) / 26)
  }
  return label
}

function sheetRange(view: StructuredSheetView) {
  const { start_row, end_row, start_col, end_col } = view.range || {}
  if (![start_row, end_row, start_col, end_col].every(Number.isSafeInteger) ||
      start_row! < 1 || start_col! < 1 || end_row! < start_row! || end_col! < start_col! ||
      end_row! > 1_048_576 || end_col! > 16_384 ||
      end_row! - start_row! + 1 > 160 || end_col! - start_col! + 1 > 64) {
    throw new Error('原件窗口行列范围无效。')
  }
  return { start_row: start_row!, end_row: end_row!, start_col: start_col!, end_col: end_col! }
}

export function structuredSheetColumns(view: StructuredSheetView): Array<{ number: number; label: string }> {
  const range = sheetRange(view)
  return Array.from({ length: range.end_col - range.start_col + 1 }, (_, index) => {
    const number = range.start_col + index
    return { number, label: sheetColumnLabel(number) }
  })
}

export function structuredSheetRows(view: StructuredSheetView): Array<{ number: number; cells: StructuredSheetDisplayCell[] }> {
  const range = sheetRange(view)
  if (!Array.isArray(view.cells) || view.cells.length > 10_240) throw new Error('原件窗口单元格结构无效。')
  const cells = new Map<string, StructuredSheetDisplayCell>()
  for (const cell of view.cells) {
    if (!cell || typeof cell !== 'object') throw new Error('原件窗口单元格坐标无效。')
    const actual = parseExcelCell(Object.hasOwn(cell, 'cell') ? cell.cell : cell.ref)
    const alias = cell.ref == null ? null : parseExcelCell(cell.ref)
    if (!actual || (cell.ref != null && (!alias || alias.address !== actual.address)) ||
        actual.row !== cell.row || actual.column !== cell.column || cells.has(actual.address)) {
      throw new Error('原件窗口单元格坐标冲突，无法准确定位。')
    }
    // Preserve actual values and display text, including zero, false and an
    // intentionally empty formatted display. Never synthesize a source value.
    cells.set(actual.address, { ...cell, ref: actual.address })
  }
  return Array.from({ length: range.end_row - range.start_row + 1 }, (_, rowIndex) => {
    const row = range.start_row + rowIndex
    const rowCells = Array.from({ length: range.end_col - range.start_col + 1 }, (_, columnIndex) => {
      const column = range.start_col + columnIndex
      const ref = `${sheetColumnLabel(column)}${row}`
      return cells.get(ref) || { row, column, ref, display: '' }
    })
    return { number: row, cells: rowCells }
  })
}
