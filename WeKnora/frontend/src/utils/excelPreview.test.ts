import assert from 'node:assert/strict';
import test from 'node:test';
import * as XLSX from 'xlsx';
import { ExcelPreviewWorkbook, EXCEL_PREVIEW_LIMITS, validateExcelArchive, validateExcelInflation } from './excelPreview';

function source(sheets: Record<string, XLSX.WorkSheet>): ArrayBuffer {
  const book = XLSX.utils.book_new();
  for (const [name, sheet] of Object.entries(sheets)) XLSX.utils.book_append_sheet(book, sheet, name);
  const value = XLSX.write(book, { type: 'array', bookType: 'xlsx' });
  return value instanceof ArrayBuffer ? value : value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength);
}

test('opens all sheet names but returns only the selected bounded viewport', () => {
  const rows = Array.from({ length: 6_000 }, (_, i) => [i + 1, `row ${i + 1}`]);
  const input = source({ First: XLSX.utils.aoa_to_sheet(rows), Second: XLSX.utils.aoa_to_sheet([['different']]) });
  const book = new ExcelPreviewWorkbook(input, 'xlsx');
  assert.deepEqual(book.sheetNames, ['First', 'Second']);
  const page = book.page('First');
  assert.equal(page.rows.length, 100);
  assert.equal(page.totalRows, 6_000);
  assert.equal(page.previewRows, 6_000);
  assert.equal(page.truncated, false);
  assert.equal(page.rows[0]!.cells[1]!.text, 'row 1');
  assert.equal(book.page('Second').rows[0]!.cells[0]!.text, 'different');
  assert.equal(book.page('First', 4_999).rows[99]!.cells[1]!.text, 'row 5000');
  assert.equal(book.page('First', 9_000).startRow, 5_900);
  assert.equal(book.page('First', 5_999).rows[99]!.cells[1]!.text, 'row 6000');
  assert.throws(() => book.page('Other'), /不存在/);
});

test('wide sheets keep column coordinates and limit rendered cells', () => {
  const sheet = XLSX.utils.aoa_to_sheet([Array.from({ length: 70 }, (_, i) => `column${i + 1}`)]);
  const book = new ExcelPreviewWorkbook(source({ Wide: sheet }), 'xlsx');
  const page = book.page('Wide', 0, 35);
  assert.equal(page.startColumn, 30);
  assert.equal(page.columns[0], 'AE');
  assert.equal(page.columns.length, 30);
  assert.equal(page.rows[0]!.cells[0]!.text, 'column31');
  assert.equal(book.page('Wide', Number.NaN, Number.POSITIVE_INFINITY).startColumn, 0);
});

test('the last page of a full sheet remains reachable with a bounded cell viewport', () => {
  const rows = Array.from({ length: 16_033 }, (_, index) => [index + 1, `record ${index + 1}`]);
  const book = new ExcelPreviewWorkbook(source({ Complete: XLSX.utils.aoa_to_sheet(rows) }), 'xlsx');
  const last = book.page('Complete', 16_032);
  assert.equal(last.totalRows, 16_033);
  assert.equal(last.previewRows, 16_033);
  assert.equal(last.startRow, 16_000);
  assert.equal(last.rows.length, 33);
  assert.equal(last.rows.at(-1)!.cells[1]!.text, 'record 16033');
  assert.equal(last.truncated, false);
  assert(last.rows.every(row => row.cells.length <= EXCEL_PREVIEW_LIMITS.pageColumns));
});

test('merged headers are clipped safely when they cross row and column pages', () => {
  const sheet = XLSX.utils.aoa_to_sheet(Array.from({ length: 102 }, () => Array(32).fill('')));
  sheet.AD99 = { t: 's', v: 'merged label' };
  sheet['!merges'] = [XLSX.utils.decode_range('AD99:AF102')];
  const book = new ExcelPreviewWorkbook(source({ Merged: sheet }), 'xlsx');
  const page = book.page('Merged', 100, 30);
  const cell = page.rows[0]!.cells[0]!;
  assert.equal(cell.text, 'merged label');
  assert.equal(cell.rowSpan, 2);
  assert.equal(cell.colSpan, 2);
  assert.equal(cell.hidden, false);
  assert.equal(page.rows[1]!.cells[0]!.hidden, true);
});

test('only inert display text and cached formula values leave the parser', () => {
  const sheet = XLSX.utils.aoa_to_sheet([['<img src=x onerror=alert(1)>', 'x'.repeat(8_000)]]);
  sheet.C1 = { t: 'n', v: 42, f: 'HYPERLINK("https://example.invalid",42)' };
  sheet.A1!.l = { Target: 'https://example.invalid/' };
  sheet['!ref'] = 'A1:C1';
  const book = new ExcelPreviewWorkbook(source({ '<img src=x>': sheet }), 'xlsx');
  const page = book.page('<img src=x>');
  assert.equal(page.rows[0]!.cells[0]!.text, '<img src=x onerror=alert(1)>');
  assert.equal(page.rows[0]!.cells[1]!.text.length, EXCEL_PREVIEW_LIMITS.cellCharacters);
  assert.equal(page.rows[0]!.cells[2]!.text, '42');
  assert.equal(JSON.stringify(page).includes('https://example.invalid'), false);
});

test('CSV and TSV retain Unicode content without evaluating formulas', () => {
  for (const extension of ['csv', 'tsv']) {
    const delimiter = extension === 'csv' ? ',' : '\t';
    const data = new TextEncoder().encode(`名称${delimiter}数值\n中文${delimiter}=1+1`);
    const book = new ExcelPreviewWorkbook(data.buffer, extension);
    const page = book.page(book.sheetNames[0]!);
    assert.equal(page.rows[1]!.cells[0]!.text, '中文');
    assert.equal(page.rows[1]!.cells[1]!.text, '=1+1');
  }
});

test('rejects oversized expanded archives from directory metadata before inflating', () => {
  // Tiny archive-shaped metadata declaring a huge member, no real huge buffer.
  const data = new ArrayBuffer(72), view = new DataView(data);
  view.setUint32(0, 0x04034b50, true);
  view.setUint32(4, 0x02014b50, true);
  view.setUint32(28, EXCEL_PREVIEW_LIMITS.entryBytes + 1, true);
  view.setUint32(50, 0x06054b50, true);
  view.setUint16(58, 1, true);
  view.setUint16(60, 1, true);
  view.setUint32(66, 4, true);
  assert.throws(() => validateExcelArchive(data), /过大/);
  assert.throws(() => validateExcelArchive(new Uint8Array([0x50, 0x4b, 0x03, 0x04]).buffer), /不完整/);
});

test('rejects a forged local allocation size even when the directory claims safe sizes', () => {
  const input = source({ Safe: XLSX.utils.aoa_to_sheet([[1]]) });
  const view = new DataView(input);
  view.setUint32(22, 2_147_483_647, true);
  assert.throws(() => validateExcelArchive(input), /不一致/);
});

test('validates real deflate output before handing an archive to SheetJS', async () => {
  const book = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(book, XLSX.utils.aoa_to_sheet([['compressed value']]), 'Data');
  const input: ArrayBuffer = XLSX.write(book, { type: 'array', bookType: 'xlsx', compression: true });
  await validateExcelInflation(input);
  // Corrupt both size declarations consistently: header-only checks pass,
  // streaming verification must still reject before the parser allocates.
  const view = new DataView(input);
  let cd = 0;
  for (let at = 0; at + 46 < input.byteLength; at++) {
    if (view.getUint32(at, true) === 0x02014b50) { cd = at; break; }
  }
  const declared = view.getUint32(cd + 24, true);
  view.setUint32(cd + 24, declared - 1, true);
  view.setUint32(22, declared - 1, true);
  validateExcelArchive(input);
  await assert.rejects(validateExcelInflation(input), /不符/);
});

test('does not let a trailing fake directory select a different parser path', () => {
  const input = source({ Safe: XLSX.utils.aoa_to_sheet([[1]]) });
  const joined = new Uint8Array(input.byteLength + 4);
  joined.set(new Uint8Array(input)); joined.set([0x50, 0x4b, 0x05, 0x06], input.byteLength);
  new DataView(joined.buffer).setUint16(input.byteLength - 2, 4, true);
  assert.throws(() => validateExcelArchive(joined.buffer), /尾部无效/);
});
