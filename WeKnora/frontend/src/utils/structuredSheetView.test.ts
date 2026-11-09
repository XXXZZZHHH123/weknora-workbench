import assert from 'node:assert/strict'
import test from 'node:test'
import { structuredSheetColumns, structuredSheetRows, type StructuredSheetCell, type StructuredSheetView } from './structuredSheetView'

const view = (cells: StructuredSheetCell[] = []): StructuredSheetView => ({
  sheet_name: 'Original', range: { start_row: 10, end_row: 11, start_col: 26, end_col: 28 }, cells,
})

test('real sheet-view cell fields populate their exact original coordinates and column labels', () => {
  // This matches read_sheet_view: the API returns `cell`, never `ref`.
  const source = view([
    { row: 10, column: 26, cell: 'Z10', display: 'Original label', value: 'Original label' },
    { row: 10, column: 27, cell: 'AA10', display: '-12.50', value: -12.5 },
    { row: 11, column: 28, cell: 'AB11', display: '0', value: 0 },
  ])
  const rows = structuredSheetRows(source)
  assert.equal(rows[0]!.cells[0]!.display, 'Original label')
  assert.equal(rows[0]!.cells[1]!.display, '-12.50')
  assert.equal(rows[1]!.cells[2]!.display, '0')
  assert.equal(rows[1]!.cells[2]!.ref, 'AB11')
  assert.deepEqual(structuredSheetColumns(source), [{ number: 26, label: 'Z' }, { number: 27, label: 'AA' }, { number: 28, label: 'AB' }])
  assert.equal(source.cells[0]!.ref, undefined, 'presentation must not rewrite the original API result')
})

test('actual empty displays, false, zero, formula caches and uncached formula text remain unchanged', () => {
  const source = view([
    { row: 10, column: 26, cell: 'Z10', value: 0 },
    { row: 10, column: 27, cell: 'AA10', value: false },
    { row: 10, column: 28, cell: 'AB10', value: 3, display: '', formula: '1+2' },
    { row: 11, column: 26, cell: 'Z11', value: 3, display: '3.00', formula: '1+2' },
    { row: 11, column: 27, cell: 'AA11', value: null, display: '=1+2', formula: '1+2' },
    { row: 11, column: 28, cell: 'AB11', value: null, display: '' },
  ])
  const displayed = structuredSheetRows(source).flatMap(row => row.cells.map(cell => cell.display ?? cell.value ?? ''))
  assert.deepEqual(displayed, [0, false, '', '3.00', '=1+2', ''])
})

test('a missing coordinate stays blank and an outside merge anchor is never copied into it', () => {
  const source = view([{ row: 9, column: 26, cell: 'Z9', display: 'Merged anchor outside this window', value: 42 }])
  const rows = structuredSheetRows(source)
  assert.equal(rows.length, 2)
  assert.equal(rows[0]!.cells.length, 3)
  assert.ok(rows.flatMap(row => row.cells).every(cell => cell.display === '' && cell.value === undefined))
})

test('legacy ref aliases are accepted only when addresses and explicit row/column agree', () => {
  assert.equal(structuredSheetRows(view([{ row: 10, column: 27, ref: 'AA10', display: 'legacy' }]))[0]!.cells[1]!.display, 'legacy')
  assert.equal(structuredSheetRows(view([{ row: 10, column: 27, cell: 'AA10', ref: '$aa$10', display: 'same' }]))[0]!.cells[1]!.display, 'same')
  for (const cells of [
    [{ row: 10, column: 27, cell: 'AA10', ref: 'AB10' }],
    [{ row: 10, column: 27, cell: 'AB10' }],
    [{ row: 10, column: 27, cell: '', ref: 'AA10' }],
    [{ row: 10, column: 27, cell: null, ref: 'AA10' }],
    [{ row: 10, column: 27 }],
    [{ row: 10, column: 27, cell: 'AA10' }, { row: 10, column: 27, cell: 'AA10', value: 999 }],
  ]) assert.throws(() => structuredSheetRows(view(cells as StructuredSheetCell[])), /坐标/)
})

test('invalid or oversized windows fail clearly instead of appearing to be empty originals', () => {
  const patches: Array<Record<string, number>> = [{ start_row: 0 }, { end_col: 16_385 }, { end_row: 9 }, { end_row: 170 }, { end_col: 90 }, { start_col: 26.5 }]
  for (const patch of patches) {
    const source = view()
    source.range = { ...source.range, ...patch }
    assert.throws(() => structuredSheetRows(source), /范围/)
  }
  assert.throws(() => structuredSheetRows({ ...view(), cells: null } as unknown as StructuredSheetView), /结构/)
})
