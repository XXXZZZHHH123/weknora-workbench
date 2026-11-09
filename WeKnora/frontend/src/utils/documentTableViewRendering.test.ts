import assert from 'node:assert/strict'
import test from 'node:test'
import { DOMParser } from '@xmldom/xmldom'

import { createChatMarkdownRenderer, renderChatMarkdown } from './chatMarkdownRenderer.ts'

test('server-selected table cells remain exact literal text in the production chat renderer', () => {
  // These are the backend's escaped presentation strings, not a second cell
  // serializer. Its Go tests independently check the original source snapshot.
  const cells = [
    ['"quoted" and \'single\'', '&quot;quoted&quot; and &apos;single&apos;'],
    ['Item|one', 'Item&vert;one'],
    [String.raw`12\34`, '12&bsol;34'],
    [String.raw`\[literal\] \(formula\)`, '&bsol;&lbrack;literal&bsol;&rbrack; &bsol;(formula&bsol;)'],
    ['**raw** _label_ [link](https://source.example/a) <script>', '&ast;&ast;raw&ast;&ast; &lowbar;label&lowbar; &lbrack;link&rbrack;(https://source.example/a) &lt;script&gt;'],
    ['$12$ ~~raw~~', String.raw`\$12\$ \~\~raw\~\~`],
    ['`literal`', '&grave;literal&grave;'],
    ['9007199254740993.125000', '9007199254740993.125000'],
    ['', ''],
  ] as const
  const markdown = '| Selected source text |\n| --- |\n' + cells.map(([, escaped]) => `| ${escaped} |`).join('\n')
  for (const streaming of [false, true]) {
    const html = renderChatMarkdown(markdown, {
      renderer: createChatMarkdownRenderer(), escapeMarkdown: text => text, sanitizeHtml: text => text,
      streaming,
    })
    assert.doesNotMatch(html, /<(?:script|del|strong|em)\b|class="katex/, 'literal source text cannot become executable HTML, emphasis or formulas')
    const document = new DOMParser().parseFromString(`<div>${html}</div>`, 'text/html')
    const rendered = Array.from(document.getElementsByTagName('td')).map(cell => cell.textContent)
    assert.deepEqual(rendered, cells.map(([original]) => original), 'visible cells preserve quotes, delimiters, blanks, backslashes and decimal precision')
  }
})
