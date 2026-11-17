/* Run the shipped catalogue formatters with offline metadata fixtures. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../src/structured_data/index.html'), 'utf8');
const names = ['fieldQualityLabel', 'fieldUnitNotice', 'updateDeclaredUnit', 'publishedQualityText', 'sheetStatusLabel'];
const context = vm.createContext({statusLabels: {auto_ready: '已识别'}});
for (const name of names) {
  const source = html.split('\n').find(line => line.startsWith(`function ${name}(`));
  assert.ok(source, `The shipped ${name} formatter exists`);
  vm.runInContext(source, context);
}
test('queryability and real field quality remain visible together', () => {
  const sheet = {published_in_dataset: true, published_quality: {
    unit_review_count: 1, text_field_count: 1, annotation_field_count: 1, no_valid_numeric_count: 0}};
  const displayed = context.sheetStatusLabel(sheet) + '；' + context.publishedQualityText(sheet);
  assert.match(displayed, /已可查询；1 项单位待核对/);
  assert.match(displayed, /1 项说明列，不可数值聚合/);
  assert.doesNotMatch(displayed, /无有效数值/);
});
test('model-declared annotations and dimensions are not presented as numeric measures', () => {
  assert.equal(context.fieldQualityLabel({field_metadata: {role: 'annotation'}}), '说明列，不可数值聚合');
  assert.match(context.fieldQualityLabel({field_metadata: {role: 'text_dimension'}}), /文本维度.*不可数值聚合/);
  assert.equal(context.fieldQualityLabel({field_quality: {observed_type: 'numeric', valid_numeric_count: 2, record_count: 3}}), '有效数值 2 / 3');
});
test('a human unit declaration is visible without claiming verbatim source evidence', () => {
  const notice = context.fieldUnitNotice({field_metadata: {unit_source: 'declared'}});
  assert.equal(notice, '单位为人工确认，未在原表找到对应文字');
  assert.equal(context.fieldUnitNotice({field_metadata: {unit_source: 'source'}}), '');
  assert.equal(context.fieldUnitNotice({}), '');
  assert.match(html, /fieldUnitNotice\(s\)\?'<br><small>'/);
});
test('manual unit edits remove stale source references before preview and save', () => {
  const column = {unit: '原单位', unit_cells: ['B1']};
  context.updateDeclaredUnit(column, '人工确认单位');
  assert.equal(column.unit, '人工确认单位');
  assert.equal(column.unit_cells, undefined);
  const unchanged = {unit: '原单位', unit_cells: ['B1']};
  context.updateDeclaredUnit(unchanged, '原单位');
  assert.deepEqual(unchanged.unit_cells, ['B1']);
  context.updateDeclaredUnit(unchanged, '');
  assert.equal(unchanged.unit, null);
  assert.equal(unchanged.unit_cells, undefined);
});
test('the mapping form sends a manually changed unit without the previous unit evidence', () => {
  const mapping = {blocks: [{date_mode: 'date', date_column: 'A', header_rows: [1], data_start_row: 2,
    data_end_row: 3, columns: [{column: 'B', metric: 'Quantity', unit: '原单位', unit_cells: ['B1'], header_cells: ['B1']}]}]};
  const editor = {value: JSON.stringify(mapping)};
  const fields = {layout: 'rows', name: '', header_rows: '1', data_start_row: '2', data_end_row: '3',
    date_source: 'row', date_mode: 'date', date_column: 'A', category_column: ''};
  const row = {dataset: {column: '0'}, querySelector(selector) {
    return {value: {column: 'B', metric: 'Quantity', unit: '人工确认单位'}[selector.match(/="([^"]+)"/)[1]]};
  }};
  const panel = {dataset: {block: '0'}, querySelector(selector) {
    return {value: fields[selector.match(/="([^"]+)"/)[1]]};
  }, querySelectorAll() { return [row]; }};
  context.$ = () => editor;
  context.document = {querySelectorAll() { return [panel]; }};
  context.invalidateMapping = () => {};
  const begin = html.indexOf('function syncFormMapping(');
  const end = html.indexOf('\nfunction parsedSamplesHTML(', begin);
  assert.ok(begin >= 0 && end > begin);
  vm.runInContext(html.slice(begin, end), context);
  context.syncFormMapping();
  const column = JSON.parse(editor.value).blocks[0].columns[0];
  assert.equal(column.unit, '人工确认单位');
  assert.equal(column.unit_cells, undefined);
});
