/* Run the shipped catalogue formatters with offline metadata fixtures. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const html = fs.readFileSync(path.join(__dirname, '../src/structured_data/index.html'), 'utf8');
const names = ['fieldQualityLabel', 'publishedQualityText', 'sheetStatusLabel'];
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
