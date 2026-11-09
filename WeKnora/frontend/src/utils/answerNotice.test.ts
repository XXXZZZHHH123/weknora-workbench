import assert from 'node:assert/strict';
import test from 'node:test';
import { extractAnswerNotice } from './answerNotice';

const notices = [
  '以下是基于本轮已读资料的初步结果，尚不代表完整覆盖全部相关内容。',
  'These are preliminary findings from the sources read so far; they may not cover all relevant material.',
];
const body = '## 结果\n\n| 日期 | 数值 |\n| --- | --- |\n| 2024-01 | 12.50 |\n\n[来源](#citation-1)\n\n    indented code\n';

test('moves only the standard notice into metadata and preserves the complete answer', () => {
  for (const notice of notices) {
    for (const separator of ['\n\n', '\r\n\r\n']) {
      assert.deepEqual(extractAnswerNotice(notice + separator + body), { content: body, notice });
    }
    assert.deepEqual(extractAnswerNotice(notice), { content: '', notice });
  }
});

test('does not remove quotations, code, JSON or a notice inside the answer', () => {
  for (const notice of notices) {
    for (const text of [
      '> ' + notice + '\n\n' + body,
      '```text\n' + notice + '\n```',
      JSON.stringify({ text: notice }),
      '引文：' + notice,
      body + '\n' + notice,
      notice + '补充内容',
      notice + '\n' + body,
      '    ' + notice + '\n\n' + body,
    ]) assert.deepEqual(extractAnswerNotice(text), { content: text, notice: '' });
  }
});

test('withholds the streaming envelope before typewriter animation starts', () => {
  for (const notice of notices) {
    const prefix = notice + '\n\n';
    for (let length = 1; length < notice.length; length++) {
      assert.equal(extractAnswerNotice(prefix.slice(0, length), true).content, '');
    }
    assert.equal(extractAnswerNotice(notice + '\n', true).content, '');
    for (let length = 1; length <= body.length; length++) {
      assert.deepEqual(extractAnswerNotice(prefix + body.slice(0, length), true), { content: body.slice(0, length), notice });
    }
  }
});

test('releases a normal answer as soon as it diverges, and keeps completed short answers', () => {
  for (const notice of notices) {
    const shortAnswer = notice.slice(0, 2);
    assert.deepEqual(extractAnswerNotice(shortAnswer), { content: shortAnswer, notice: '' });
    const differentAnswer = shortAnswer + '!';
    assert.deepEqual(extractAnswerNotice(differentAnswer, true), { content: differentAnswer, notice: '' });
  }
  assert.deepEqual(extractAnswerNotice(''), { content: '', notice: '' });
});
