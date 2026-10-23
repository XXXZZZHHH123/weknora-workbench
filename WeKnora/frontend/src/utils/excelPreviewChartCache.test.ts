import assert from 'node:assert/strict';
import test from 'node:test';
import JSZip from 'jszip';
import * as XLSX from 'xlsx';
import { ExcelPreviewArchive, EXCEL_PREVIEW_LIMITS } from './excelPreview';

const C = 'http://schemas.openxmlformats.org/drawingml/2006/chart';
const A = 'http://schemas.openxmlformats.org/drawingml/2006/main';
const D = 'http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing';
const S = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const P = 'http://schemas.openxmlformats.org/package/2006/relationships';

function chart(title: string, count = 4, date = false) {
  const kind = date ? 'dateAx' : 'catAx';
  return `<c:chartSpace xmlns:c="${C}" xmlns:a="${A}"><c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>${title}</a:t></a:r></a:p></c:rich></c:tx></c:title><c:plotArea><c:lineChart><c:ser><c:tx><c:v>Values</c:v></c:tx><c:cat><c:numRef><c:numCache><c:ptCount val="${count}"/><c:pt idx="0"><c:v>45292</c:v></c:pt></c:numCache></c:numRef></c:cat><c:val><c:numRef><c:numCache><c:ptCount val="${count}"/><c:pt idx="0"><c:v>1</c:v></c:pt></c:numCache></c:numRef></c:val></c:ser><c:axId val="1"/><c:axId val="2"/></c:lineChart><c:${kind}><c:axId val="1"/><c:axPos val="b"/><c:crossAx val="2"/></c:${kind}><c:valAx><c:axId val="2"/><c:axPos val="l"/><c:crossAx val="1"/></c:valAx></c:plotArea></c:chart></c:chartSpace>`;
}
async function fixture(charts = [chart('one'), chart('two'), chart('three')]) {
  const book = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(book, XLSX.utils.aoa_to_sheet([['source', 7]]), 'Data');
  XLSX.utils.book_append_sheet(book, XLSX.utils.aoa_to_sheet([['other', 9]]), 'Other');
  const zip = await JSZip.loadAsync(XLSX.write(book, { type: 'array', bookType: 'xlsx' }));
  const original = await zip.file('xl/worksheets/sheet1.xml')!.async('string');
  zip.file('xl/worksheets/sheet1.xml', original.replace('</worksheet>', '<drawing r:id="drawing"/></worksheet>'));
  zip.file('xl/worksheets/_rels/sheet1.xml.rels', `<Relationships xmlns="${P}"><Relationship Id="drawing" Type="${R}/drawing" Target="../drawings/drawing1.xml"/></Relationships>`);
  const anchors = charts.map((_, i) => `<xdr:twoCellAnchor><xdr:from><xdr:row>${i}</xdr:row></xdr:from><xdr:graphicFrame><a:graphic><a:graphicData uri="${C}"><c:chart r:id="chart${i}"/></a:graphicData></a:graphic></xdr:graphicFrame></xdr:twoCellAnchor>`).join('');
  zip.file('xl/drawings/drawing1.xml', `<xdr:wsDr xmlns:xdr="${D}" xmlns:a="${A}" xmlns:c="${C}" xmlns:r="${R}">${anchors}</xdr:wsDr>`);
  zip.file('xl/drawings/_rels/drawing1.xml.rels', `<Relationships xmlns="${P}">${charts.map((_, i) => `<Relationship Id="chart${i}" Type="${R}/chart" Target="../charts/chart${i}.xml"/>`).join('')}</Relationships>`);
  charts.forEach((source, i) => zip.file(`xl/charts/chart${i}.xml`, source));
  return ExcelPreviewArchive.open(await zip.generateAsync({ type: 'arraybuffer', compression: 'DEFLATE' }), 'xlsx');
}
async function inflationCount(run: (count: () => number) => Promise<void>) {
  const original = globalThis.DecompressionStream; let count = 0;
  globalThis.DecompressionStream = class extends original {
    constructor(format: CompressionFormat) { super(format); count++; }
  };
  try { await run(() => count); } finally { globalThis.DecompressionStream = original; }
}

test('chart hits do not reinflate, and two-entry LRU refreshes actual recency', async () => {
  const archive = await fixture(), list = await archive.chartList('Data');
  const ids = list.charts.map(item => item.id);
  await inflationCount(async count => {
    await archive.chart('Data', ids[0]!); assert.equal(count(), 1);
    await archive.chart('Data', ids[1]!); assert.equal(count(), 2);
    await archive.chart('Data', ids[0]!); assert.equal(count(), 2);
    await archive.chart('Data', ids[2]!); assert.equal(count(), 3);
    await archive.chart('Data', ids[0]!); assert.equal(count(), 3);
    await archive.chart('Data', ids[1]!); assert.equal(count(), 4);
  });
});

test('chart entry releases full grid and returning rebuilds the correct original sheet', async () => {
  const archive = await fixture(), before = await archive.page('Data');
  const internals = archive as unknown as { selected: unknown; selectedSheet: string };
  assert.ok(internals.selected); assert.equal(internals.selectedSheet, 'Data');
  const id = (await archive.chartList('Data')).charts[0]!.id;
  await archive.chart('Data', id);
  assert.equal(internals.selected, null); assert.equal(internals.selectedSheet, '');
  const after = await archive.page('Data');
  assert.deepEqual(after.rows, before.rows); assert.equal(after.rows[0]!.cells[1]!.text, '7');
  assert.ok(internals.selected);
});

test('sparse array slots and date coordinates count toward the byte limit', async () => {
  const archive = await fixture([chart('too large to retain', 100_000, true)]);
  const id = (await archive.chartList('Data')).charts[0]!.id;
  await inflationCount(async count => {
    const result = await archive.chart('Data', id);
    assert.equal(result.series[0]!.values.length, 100_000);
    assert.equal(result.series[0]!.categoryTimes!.length, 100_000);
    await archive.chart('Data', id); assert.equal(count(), 2, 'over-budget result is parsed again instead of cached');
  });
  const internals = archive as unknown as { chartCacheBytes: number };
  assert.equal(internals.chartCacheBytes, 0);
});

test('total retained-byte ceiling evicts before the two-entry count limit', async () => {
  const archive = await fixture([chart('large one', 70_000), chart('large two', 70_000)]);
  const ids = (await archive.chartList('Data')).charts.map(item => item.id);
  await inflationCount(async count => {
    await archive.chart('Data', ids[0]!); await archive.chart('Data', ids[1]!);
    const internals = archive as unknown as { chartCacheBytes: number };
    assert.ok(internals.chartCacheBytes <= EXCEL_PREVIEW_LIMITS.chartCacheBytes);
    await archive.chart('Data', ids[0]!); assert.equal(count(), 3, 'byte ceiling retained only one of these large charts');
  });
});

test('cache is cleared on sheet change, never crosses sources, and cannot authorize an unrelated id', async () => {
  const first = await fixture([chart('first source')]), second = await fixture([chart('second source')]);
  const id = (await first.chartList('Data')).charts[0]!.id;
  assert.equal((await first.chart('Data', id)).title, 'first source');
  assert.equal((await second.chart('Data', id)).title, 'second source');
  await first.page('Other');
  await assert.rejects(first.chart('Other', id), /不属于/);
  await assert.rejects(first.chart('Data', 'xl/worksheets/sheet1.xml'), /不属于/);
  await inflationCount(async count => {
    assert.equal((await first.chart('Data', id)).title, 'first source');
    assert.equal(count(), 1, 'source binding is known but sheet switch discarded the parsed DTO');
  });
});

test('concurrent completion of one chart does not double count retained bytes', async () => {
  const sequential = await fixture(), parallel = await fixture();
  const id = (await sequential.chartList('Data')).charts[0]!.id;
  await parallel.chartList('Data');
  await sequential.chart('Data', id);
  await Promise.all([parallel.chart('Data', id), parallel.chart('Data', id), parallel.chart('Data', id)]);
  const bytes = (archive: ExcelPreviewArchive) => (archive as unknown as { chartCacheBytes: number }).chartCacheBytes;
  assert.equal(bytes(parallel), bytes(sequential));
  assert.ok(bytes(parallel) <= EXCEL_PREVIEW_LIMITS.chartCacheBytes);
});
