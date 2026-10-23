import assert from 'node:assert/strict';
import test from 'node:test';
import JSZip from 'jszip';
import * as XLSX from 'xlsx';
import { ExcelPreviewArchive } from './excelPreview';
import { parseExcelChart, parseDrawingCharts, sheetDrawingScanner } from './excelPreviewCharts';

const C = 'http://schemas.openxmlformats.org/drawingml/2006/chart';
const A = 'http://schemas.openxmlformats.org/drawingml/2006/main';
const D = 'http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing';
const S = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main';
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const P = 'http://schemas.openxmlformats.org/package/2006/relationships';
const axes = '<c:catAx><c:axId val="1"/><c:axPos val="b"/><c:crossAx val="2"/></c:catAx><c:valAx><c:axId val="2"/><c:axPos val="l"/><c:crossAx val="1"/></c:valAx>';
const points = '<c:ptCount val="4"/><c:pt idx="0"><c:v>0</c:v></c:pt><c:pt idx="2"><c:v>#N/A</c:v></c:pt><c:pt idx="3"><c:v>42</c:v></c:pt>';
function serie(name = 'actual', ref = 'Data!$B$1:$B$4') {
  return `<c:ser><c:tx><c:v>${name}</c:v></c:tx><c:spPr><a:ln><a:solidFill><a:srgbClr val="ABC123"/></a:solidFill></a:ln></c:spPr><c:cat><c:strRef><c:f>Data!$A$1:$A$4</c:f><c:strCache><c:ptCount val="4"/><c:pt idx="0"><c:v>First</c:v></c:pt><c:pt idx="3"><c:v>Last</c:v></c:pt></c:strCache></c:strRef></c:cat><c:val><c:numRef><c:f>${ref}</c:f><c:numCache>${points}</c:numCache></c:numRef></c:val></c:ser>`;
}
export function chartFixture(group = 'lineChart') {
  return `<c:chartSpace xmlns:c="${C}" xmlns:a="${A}"><c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>Saved chart</a:t></a:r></a:p></c:rich></c:tx></c:title><c:plotArea><c:${group}><c:grouping val="standard"/>${serie()}<c:axId val="1"/><c:axId val="2"/></c:${group}>${axes}</c:plotArea></c:chart></c:chartSpace>`;
}
const drawing = `<xdr:wsDr xmlns:xdr="${D}" xmlns:a="${A}" xmlns:c="${C}" xmlns:r="${R}"><xdr:twoCellAnchor><xdr:from><xdr:col>0</xdr:col><xdr:row>7</xdr:row></xdr:from><xdr:graphicFrame><a:graphic><a:graphicData uri="${C}"><c:chart r:id="chart1"/></a:graphicData></a:graphic></xdr:graphicFrame></xdr:twoCellAnchor></xdr:wsDr>`;
async function archiveFixture() {
  const workbook = XLSX.utils.book_new(); XLSX.utils.book_append_sheet(workbook, XLSX.utils.aoa_to_sheet([['value', 1]]), 'Data');
  const zip = await JSZip.loadAsync(XLSX.write(workbook, { type: 'array', bookType: 'xlsx' }));
  zip.file('xl/worksheets/sheet1.xml', `<worksheet xmlns="${S}" xmlns:r="${R}"><sheetData><row r="1"><c r="A1" t="str"><v>Data</v></c></row></sheetData><drawing r:id="draw1"/></worksheet>`);
  zip.file('xl/worksheets/_rels/sheet1.xml.rels', `<Relationships xmlns="${P}"><Relationship Id="draw1" Type="${R}/drawing" Target="../drawings/drawing1.xml"/></Relationships>`);
  zip.file('xl/drawings/drawing1.xml', drawing);
  zip.file('xl/drawings/_rels/drawing1.xml.rels', `<Relationships xmlns="${P}"><Relationship Id="chart1" Type="${R}/chart" Target="../charts/chart1.xml"/></Relationships>`);
  zip.file('xl/charts/chart1.xml', chartFixture());
  return zip;
}
const bytes = (zip: JSZip) => zip.generateAsync({ type: 'arraybuffer', compression: 'DEFLATE' });

test('reads cached sparse points, real zero and errors without recomputation', () => {
  const result = parseExcelChart(chartFixture(), 'chart');
  assert.equal(result.title, 'Saved chart');
  assert.deepEqual(result.series[0]!.values, [0, null, null, 42]);
  assert.deepEqual(result.series[0]!.categories, ['First', null, null, 'Last']);
  assert.equal(result.series[0]!.color, '#ABC123');
  assert.equal(result.series[0]!.axisId, '2'); assert.equal(result.axes[0]!.crossAxisId, '2');
});
test('missing caches never fetch references and external formulas only expose saved values', () => {
  const external = chartFixture().replace('Data!$B$1:$B$4', '[external.xlsx]Data!$B$1:$B$4');
  assert.ok(parseExcelChart(external, 'id').notices.some(text => text.includes('未访问外部')));
  const missing = chartFixture().replace(`<c:numCache>${points}</c:numCache>`, '');
  const parsed = parseExcelChart(missing, 'id');
  assert.deepEqual(parsed.series[0]!.values, []);
  assert.ok(parsed.notices.some(text => text.includes('没有已保存')));
});
test('keeps combination types, group identity and secondary value axis', () => {
  const xml = chartFixture().replace('</c:lineChart>', `</c:lineChart><c:barChart><c:barDir val="bar"/><c:grouping val="stacked"/>${serie('second')}<c:axId val="1"/><c:axId val="3"/></c:barChart>`)
    .replace('</c:plotArea>', '<c:valAx><c:axId val="3"/><c:axPos val="r"/><c:crossAx val="1"/></c:valAx></c:plotArea>');
  const parsed = parseExcelChart(xml, 'id');
  assert.deepEqual(parsed.series.map(item => [item.type, item.axisId]), [['line', '2'], ['bar', '3']]);
  assert.equal(parsed.series[1]!.horizontal, true); assert.equal(parsed.series[1]!.grouping, 'stacked');
  assert.notEqual(parsed.series[0]!.groupId, parsed.series[1]!.groupId);
});
test('date axes preserve real uneven date spacing and workbook epoch', () => {
  const xml = chartFixture().replace('<c:catAx>', '<c:dateAx><c:numFmt formatCode="yyyy-mm-dd"/>').replace('</c:catAx>', '</c:dateAx>')
    .replace(/<c:strRef><c:f>Data!\$A\$1:\$A\$4<\/c:f><c:strCache>.*?<\/c:strCache><\/c:strRef>/,
      '<c:numRef><c:f>Data!$A$1:$A$4</c:f><c:numCache><c:ptCount val="4"/><c:pt idx="0"><c:v>1</c:v></c:pt><c:pt idx="1"><c:v>2</c:v></c:pt><c:pt idx="3"><c:v>10</c:v></c:pt></c:numCache></c:numRef>');
  const parsed = parseExcelChart(xml, 'id', true), dates = parsed.series[0]!.categoryTimes!;
  assert.equal(parsed.axes[0]!.type, 'date');
  assert.deepEqual(parsed.series[0]!.categories, ['1904-01-02', '1904-01-03', null, '1904-01-11']);
  assert.deepEqual(dates, [Date.UTC(1904, 0, 2), Date.UTC(1904, 0, 3), null, Date.UTC(1904, 0, 11)]);
  assert.equal(dates[3]! - dates[1]!, 8 * 86_400_000);
});
test('missing date caches never turn synthetic category indexes into Excel dates', () => {
  const xml = chartFixture().replace('<c:catAx>', '<c:dateAx>').replace('</c:catAx>', '</c:dateAx>')
    .replace(/<c:strRef><c:f>Data!\$A\$1:\$A\$4<\/c:f><c:strCache>.*?<\/c:strCache><\/c:strRef>/,
      '<c:numRef><c:f>Data!$A$1:$A$4</c:f></c:numRef>');
  const parsed = parseExcelChart(xml, 'id');
  assert.deepEqual(parsed.series[0]!.categories, [null, null, null, null]);
  assert.deepEqual(parsed.series[0]!.categoryTimes, [null, null, null, null]);
  assert.ok(parsed.notices.some(message => message.includes('不以点序号补造日期')));
});
test('unknown types, missing axes and logarithmic scales are explicit instead of guessed', () => {
  assert.equal(parseExcelChart(chartFixture('scatterChart'), 'id').series.length, 0);
  assert.equal(parseExcelChart(chartFixture().replace('<c:axId val="2"/></c:lineChart>', '<c:axId val="99"/></c:lineChart>'), 'id').series.length, 0);
  const log = chartFixture().replace('<c:valAx>', '<c:valAx><c:scaling><c:logBase val="10"/></c:scaling>');
  assert.equal(parseExcelChart(log, 'id').series.length, 0);
});
test('rejects malformed indexes, point explosions, DTD and incorrect namespaces', () => {
  assert.throws(() => parseExcelChart(chartFixture().replace('idx="3"', 'idx="0"'), 'id'), /索引/);
  assert.throws(() => parseExcelChart(chartFixture().replace('val="4"', 'val="100001"'), 'id'), /点数/);
  assert.throws(() => parseExcelChart('<!DOCTYPE chartSpace [<!ENTITY x "boom">]>' + chartFixture(), 'id'), /DTD|实体/);
  assert.throws(() => parseExcelChart(chartFixture().replace(`xmlns:c="${C}"`, 'xmlns:c="urn:fake"'), 'id'), /命名空间/);
});
test('streaming worksheet scanner recognizes only direct real drawing IDs', () => {
  const ids: string[] = [], scanner = sheetDrawingScanner(ids);
  for (const piece of [`<worksheet xmlns="${S}" xmlns:r="${R}"><sheetData>`, '<row/>'.repeat(20_000), '</sheetData><drawing r:id="draw1"/></worksheet>']) scanner.write(piece);
  scanner.close(); assert.deepEqual(ids, ['draw1']);
  assert.deepEqual(parseDrawingCharts(drawing), [{ relationshipId: 'chart1', anchorRow: 8 }]);
  assert.deepEqual(parseDrawingCharts(drawing.replace(`uri="${C}"`, 'uri="urn:not-chart"')), []);
  assert.throws(() => sheetDrawingScanner([]).write(`<!DOCTYPE worksheet><worksheet xmlns="${S}"/>`).close(), /DTD/);
  assert.throws(() => sheetDrawingScanner([]).write(`<worksheet xmlns="${S}" xmlns:r="${R}" xmlns:q="${R}"><drawing r:id="one" q:id="two"/></worksheet>`).close(), /歧义/);
});
test('archive resolves real worksheet/drawing/chart chain and updates selected title', async () => {
  const archive = await ExcelPreviewArchive.open(await bytes(await archiveFixture()), 'xlsx');
  const list = await archive.chartList('Data');
  assert.deepEqual(list.charts, [{ id: 'xl/charts/chart1.xml', title: '图表 1', anchorRow: 8 }]);
  assert.equal((await archive.chart('Data', list.charts[0]!.id)).title, 'Saved chart');
  assert.equal((await archive.chartList('Data')).charts[0]!.title, 'Saved chart');
  await assert.rejects(archive.chart('Data', 'xl/worksheets/sheet1.xml'), /不属于/);
});
test('orphan relations do not authorize charts and external relationships are denied', async () => {
  const zip = await archiveFixture();
  zip.file('xl/worksheets/sheet1.xml', `<worksheet xmlns="${S}"><sheetData/></worksheet>`);
  const archive = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.deepEqual((await archive.chartList('Data')).charts, []);
  zip.file('xl/worksheets/sheet1.xml', `<worksheet xmlns="${S}" xmlns:r="${R}"><drawing r:id="draw1"/></worksheet>`);
  zip.file('xl/drawings/_rels/drawing1.xml.rels', `<Relationships xmlns="${P}"><Relationship Id="chart1" Type="${R}/chart" TargetMode="External" Target="https://example.invalid/x"/></Relationships>`);
  await assert.rejects((await ExcelPreviewArchive.open(await bytes(zip), 'xlsx')).chartList('Data'), /外部/);
});
test('directory does not inflate selected chart until opened and per-chart budget is enforced', async () => {
  const zip = await archiveFixture(); zip.file('xl/charts/chart1.xml', 'x'.repeat(4 * 1024 * 1024 + 1));
  const archive = await ExcelPreviewArchive.open(await bytes(zip), 'xlsx');
  assert.equal((await archive.chartList('Data')).charts.length, 1);
  await assert.rejects(archive.chart('Data', 'xl/charts/chart1.xml'), /过大/);
});
