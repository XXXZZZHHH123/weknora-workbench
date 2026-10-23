import assert from 'node:assert/strict';
import test from 'node:test';
import JSZip from 'jszip';
import * as XLSX from 'xlsx';
import { ExcelPreviewArchive, validateExcelInflation } from './excelPreview';

async function fixture() {
  const book = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(book, XLSX.utils.aoa_to_sheet([['first', 12.5]]), 'First');
  XLSX.utils.book_append_sheet(book, XLSX.utils.aoa_to_sheet([['second', 24]]), 'Second');
  book.Sheets.First!.B1!.z = '0.00';
  return JSZip.loadAsync(XLSX.write(book, { type: 'array', bookType: 'xlsx', bookSST: true }));
}

async function bytes(zip: JSZip) { return zip.generateAsync({ type: 'arraybuffer', compression: 'DEFLATE' }); }

function changeDeclaredSize(data: ArrayBuffer, namePrefix: string, expanded: number) {
  const view = new DataView(data), decoder = new TextDecoder();
  for (let offset = 0; offset + 46 < data.byteLength; offset++) {
    if (view.getUint32(offset, true) !== 0x02014b50) continue;
    const size = view.getUint16(offset + 28, true);
    const name = decoder.decode(new Uint8Array(data, offset + 46, size));
    if (!name.startsWith(namePrefix)) continue;
    view.setUint32(offset + 24, expanded, true);
    view.setUint32(view.getUint32(offset + 42, true) + 22, expanded, true);
  }
}

test('selective archive resolves actual relationship targets after their order changes', async () => {
  const zip = await fixture();
  const path = 'xl/_rels/workbook.xml.rels';
  const rels = await zip.file(path)!.async('string');
  zip.file(path, rels.replace('worksheets/sheet1.xml', 'worksheets/temporary.xml')
    .replace('worksheets/sheet2.xml', 'worksheets/sheet1.xml')
    .replace('worksheets/temporary.xml', 'worksheets/sheet2.xml'));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.deepEqual(book.sheetNames, ['First', 'Second']);
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'second');
  assert.equal((await book.page('Second')).rows[0]!.cells[0]!.text, 'first');
  await assert.rejects(book.page('missing'), /不存在/);
});

test('does not inflate unselected worksheets or unrelated members from a large archive', async () => {
  const zip = await fixture();
  for (let i = 0; i < 6; i++) zip.file(`unselected/large-${i}.xml`, 'unused');
  const data = await bytes(zip);
  // Over 300 MB is declared in independent entries. The unused synthetic
  // streams deliberately mismatch that declaration to prove they are not read.
  changeDeclaredSize(data, 'unselected/large-', 50 * 1024 * 1024);
  changeDeclaredSize(data, 'xl/worksheets/sheet2.xml', 30 * 1024 * 1024);
  await assert.rejects(validateExcelInflation(data), /过大/);
  const book = await ExcelPreviewArchive.open(data, 'xlsx');
  assert.deepEqual(book.sheetNames, ['First', 'Second']);
  const first = await book.page('First');
  assert.equal(first.rows[0]!.cells[0]!.text, 'first');
  assert.equal(first.rows[0]!.cells[1]!.text, '12.50');
  await assert.rejects(book.page('Second'), /不完整|不符/);
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
});

test('follows the office-document relationship when the workbook part moves', async () => {
  const zip = await fixture();
  const workbook = await zip.file('xl/workbook.xml')!.async('uint8array');
  const rels = await zip.file('xl/_rels/workbook.xml.rels')!.async('string');
  zip.file('custom/book.xml', workbook);
  zip.file('custom/_rels/book.xml.rels', rels.replaceAll('Target="', 'Target="../xl/'));
  const root = await zip.file('_rels/.rels')!.async('string');
  zip.file('_rels/.rels', root.replace('xl/workbook.xml', 'custom/book.xml'));
  zip.remove('xl/workbook.xml'); zip.remove('xl/_rels/workbook.xml.rels');
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
});

for (const target of ['../../../../escape.xml', 'https://invalid.example/sheet.xml', 'worksheets/%2e%2e/%2e%2e/%2e%2e/escape.xml']) {
  test('rejects worksheet target outside the package: ' + target, async () => {
    const zip = await fixture();
    const path = 'xl/_rels/workbook.xml.rels';
    zip.file(path, (await zip.file(path)!.async('string')).replace('worksheets/sheet1.xml', target));
    await assert.rejects(ExcelPreviewArchive.open(await bytes(zip), 'xlsx'), /路径|来源|工作表|关系/);
  });
}

test('rejects external relationships for public strings and does not substitute a worksheet', async () => {
  const zip = await fixture();
  const path = 'xl/_rels/workbook.xml.rels';
  zip.file(path, (await zip.file(path)!.async('string')).replace('Target="sharedStrings.xml"', 'TargetMode="External" Target="https://invalid.example/sharedStrings.xml"'));
  await assert.rejects(ExcelPreviewArchive.open(await bytes(zip), 'xlsx'), /来源|关系/);
});

test('preserves the date system and readable cells while discarding drawings and external links', async () => {
  const zip = await fixture();
  const path = 'xl/workbook.xml';
  zip.file(path, (await zip.file(path)!.async('string')).replace('<workbookPr', '<workbookPr date1904="1"'));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.equal((await book.page('First')).rows[0]!.cells[1]!.text, '12.50');
});

test('keeps non-ZIP CSV on the bounded fallback path', async () => {
  const data = new TextEncoder().encode('name,value\nitem,12').buffer;
  const book = await ExcelPreviewArchive.open(data, 'csv');
  assert.equal((await book.page(book.sheetNames[0]!)).rows[1]!.cells[1]!.text, '12');
});

for (const magic of [0x08074b50, 0x06054b50, 0x02014b50]) {
  test('rejects a non-local ZIP signature before any format fallback: ' + magic.toString(16), async () => {
    const data = await bytes(await fixture());
    new DataView(data).setUint32(0, magic, true);
    await assert.rejects(ExcelPreviewArchive.open(data, 'xlsx'), /文件头无效/);
    await assert.rejects(ExcelPreviewArchive.open(data, 'xls'), /文件头无效/);
  });
}

test('requires ZIP/OPC metadata for XLSX even if SheetJS could guess a text workbook', async () => {
  await assert.rejects(ExcelPreviewArchive.open(new TextEncoder().encode('a,b\n1,2').buffer, 'xlsx'), /有效.*ZIP/);
});

test('keeps the workbook 1904 date origin in a selected-sheet package', async () => {
  const input = XLSX.utils.book_new();
  const sheet = XLSX.utils.aoa_to_sheet([[1]]); sheet.A1!.z = 'yyyy-mm-dd';
  XLSX.utils.book_append_sheet(input, sheet, 'Dates');
  input.Workbook = { WBProps: { date1904: true } };
  const data = XLSX.write(input, { type: 'array', bookType: 'xlsx', compression: true });
  const book = await ExcelPreviewArchive.open(data, 'xlsx');
  assert.equal((await book.page('Dates')).rows[0]!.cells[0]!.text, '1904-01-02');
});

test('ignores a preceding extension id and binds the real relationships-namespace id', async () => {
  const zip = await fixture(), path = 'xl/workbook.xml';
  const xml = await zip.file(path)!.async('string');
  const ids = [...xml.matchAll(/r:id="([^"]+)"/g)].map(match => match[1]!);
  zip.file(path, xml.replace('name="First"', `xmlns:probe="urn:preview-probe" probe:id="${ids[1]}" name="First"`));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
});

test('supports a different prefix bound to the correct relationships namespace', async () => {
  const zip = await fixture(), path = 'xl/workbook.xml';
  zip.file(path, (await zip.file(path)!.async('string')).replace('xmlns:r=', 'xmlns:actual=').replaceAll('r:id=', 'actual:id='));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
});

test('rejects contradictory ids even when both prefixes use the relationships namespace', async () => {
  const zip = await fixture(), path = 'xl/workbook.xml';
  const xml = await zip.file(path)!.async('string');
  const ids = [...xml.matchAll(/r:id="([^"]+)"/g)].map(match => match[1]!);
  zip.file(path, xml.replace('name="First"', `xmlns:probe="http://schemas.openxmlformats.org/officeDocument/2006/relationships" probe:id="${ids[1]}" name="First"`));
  await assert.rejects(ExcelPreviewArchive.open(await bytes(zip), 'xlsx'), /关系.*无效|关系.*矛盾/);
});

test('ignores extension-namespace sheet elements outside the real sheets structure', async () => {
  const zip = await fixture(), path = 'xl/workbook.xml';
  const xml = await zip.file(path)!.async('string');
  zip.file(path, xml.replace('<sheets>', '<sheets><probe:sheet xmlns:probe="urn:preview-probe" name="Phantom" probe:id="invalid"/>'));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.deepEqual(book.sheetNames, ['First', 'Second']);
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
});

test('keeps data worksheets available when the same workbook also has a chart-only tab', async () => {
  const zip = await fixture(), path = 'xl/_rels/workbook.xml.rels';
  const rels = await zip.file(path)!.async('string');
  zip.file(path, rels.replace('/worksheet" Target="worksheets/sheet2.xml"', '/chartsheet" Target="worksheets/sheet2.xml"'));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.deepEqual(book.sheetNames, ['First']);
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
});

test('accepts a standard binary XLS header and rejects CFB ZIP-parser version tricks', async () => {
  const input = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(input, XLSX.utils.aoa_to_sheet([[17]]), 'Binary');
  const data = XLSX.write(input, { type: 'array', bookType: 'biff8' });
  const book = await ExcelPreviewArchive.open(data, 'xls');
  assert.equal((await book.page('Binary')).rows[0]!.cells[0]!.text, '17');
  const view = new DataView(data);
  view.setUint16(24, 0, true); view.setUint16(26, 0, true);
  await assert.rejects(ExcelPreviewArchive.open(data, 'xls'), /复合文件头无效/);
});

test('preserves a percent-encoded ZIP member identity and rejects a conflicting decoded alias', async () => {
  const zip = await fixture(), path = 'xl/_rels/workbook.xml.rels';
  const first = await zip.file('xl/worksheets/sheet1.xml')!.async('uint8array');
  const second = await zip.file('xl/worksheets/sheet2.xml')!.async('uint8array');
  zip.file('xl/worksheets/sheet%201.xml', first);
  zip.file(path, (await zip.file(path)!.async('string')).replace('worksheets/sheet1.xml', 'worksheets/sheet%201.xml'));
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.equal((await book.page('First')).rows[0]!.cells[0]!.text, 'first');
  zip.file('xl/worksheets/sheet 1.xml', second);
  await assert.rejects(ExcelPreviewArchive.open(await bytes(zip), 'xlsx'), /编码歧义/);
});

test('keeps more than 136 worksheet names and opens the final sheet beyond row 5000', async () => {
  const input = XLSX.utils.book_new();
  for (let index = 0; index < 137; index++) {
    const sheet = index === 136
      ? XLSX.utils.aoa_to_sheet(Array.from({ length: 6_003 }, (_, row) => [row + 1]))
      : XLSX.utils.aoa_to_sheet([[index]]);
    XLSX.utils.book_append_sheet(input, sheet, `Sheet ${index + 1}`);
  }
  const book = await ExcelPreviewArchive.open(XLSX.write(input, { type: 'array', bookType: 'xlsx', compression: true }), 'xlsx');
  assert.equal(book.sheetNames.length, 137);
  const last = await book.page(book.sheetNames.at(-1)!, 6_002);
  assert.equal(last.totalRows, 6_003);
  assert.equal(last.rows.at(-1)!.number, 6_003);
  assert.equal(last.rows.at(-1)!.cells[0]!.text, '6003');
  assert.equal(last.truncated, false);
});

test('preserves saved formula results without loading their other-sheet dependencies', async () => {
  const zip = await fixture();
  const namespace = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
  zip.file('xl/worksheets/sheet1.xml', `<worksheet xmlns="${namespace}"><dimension ref="A1:H1"/><sheetData><row r="1">
    <c r="A1"><f>Second!B1+1</f><v>25</v></c>
    <c r="B1"><f t="shared" si="0" ref="B1:C1">A1+1</f><v>26</v></c>
    <c r="C1"><f t="shared" si="0"/><v>27</v></c>
    <c r="D1"><f t="array" ref="D1:D1">SUM(A1:C1)</f><v>78</v></c>
    <c r="E1"><f>1-1</f><v>0</v></c>
    <c r="F1" t="b"><f>FALSE()</f><v>0</v></c>
    <c r="G1" t="e"><f>NA()</f><v>#N/A</v></c>
    <c r="H1" t="str"><f>IF(A1&gt;0,&quot;&quot;,&quot;value&quot;)</f><v></v></c>
  </row></sheetData></worksheet>`);
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  const page = await book.page('First');
  assert.deepEqual(page.rows[0]!.cells.map(cell => cell.text), ['25', '26', '27', '78', '0', 'FALSE', '#N/A', '']);
});

test('does not invent zero when an original formula has no saved calculation result', async () => {
  const zip = await fixture();
  zip.file('xl/worksheets/sheet1.xml', '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:B1"/><sheetData><row r="1"><c r="A1"><f>Second!A1</f></c><c r="B1"><f>0</f><v>0</v></c></row></sheetData></worksheet>');
  const book = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.deepEqual((await book.page('First')).rows[0]!.cells.map(cell => cell.text), ['', '0']);
});
