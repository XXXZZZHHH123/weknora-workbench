import * as XLSX from 'xlsx';
import JSZip from 'jszip';
import { DOMParser, type Element as XmlElement } from '@xmldom/xmldom';
import { EXCEL_CHART_LIMITS, parseDrawingCharts, parseExcelChart, sheetDrawingScanner,
  type ExcelPreviewChart, type ExcelPreviewChartList } from './excelPreviewCharts';

// Preview is deliberately separate from structured import. It never evaluates
// formulas, loads remote links or publishes data, and keeps only one sheet.
export const EXCEL_PREVIEW_LIMITS = Object.freeze({
  fileBytes: 100 * 1024 * 1024,
  expandedBytes: 128 * 1024 * 1024,
  archiveExpandedBytes: 1024 * 1024 * 1024,
  metadataBytes: 8 * 1024 * 1024,
  entryBytes: 64 * 1024 * 1024,
  entries: 20_000,
  // Browse the complete selected sheet; the DOM remains a 100 × 30 viewport.
  // This is the XLSX format boundary, not a product preview cutoff.
  rows: 1_048_576,
  pageRows: 100,
  pageColumns: 30,
  cellCharacters: 4_096,
  chartCacheEntries: 2,
  chartCacheBytes: 8 * 1024 * 1024,
});

export type ExcelPreviewCell = { text: string; rowSpan: number; colSpan: number; hidden: boolean };
export type ExcelPreviewPage = {
  sheet: string; startRow: number; startColumn: number;
  totalRows: number; previewRows: number; totalColumns: number;
  columns: string[]; rows: { number: number; cells: ExcelPreviewCell[] }[];
  truncated: boolean;
};

/** Inspect ZIP metadata before SheetJS inflates it. No archive extraction here. */
type ZipMember = { name: string; start: number; compressed: number; expanded: number; method: number };
function inspectExcelArchive(data: ArrayBuffer, expandedLimit = EXCEL_PREVIEW_LIMITS.expandedBytes): ZipMember[] {
  if (data.byteLength > EXCEL_PREVIEW_LIMITS.fileBytes) throw new Error('文件超过 100 MB，请下载原件查看。');
  const view = new DataView(data);
  if (data.byteLength >= 8 && view.getUint32(0, true) === 0xe011cfd0 && view.getUint32(4, true) === 0xe11ab1a1) {
    const version = data.byteLength >= 512 ? view.getUint16(26, true) : 0;
    if (![3, 4].includes(version) || view.getUint16(28, true) !== 0xfffe
        || view.getUint16(30, true) !== (version === 3 ? 9 : 12)
        || view.getUint16(32, true) !== 6) throw new Error('旧版 Excel 复合文件头无效。');
    return [];
  }
  if (data.byteLength >= 2 && view.getUint16(0, true) === 0x4b50
      && (data.byteLength < 4 || view.getUint32(0, true) !== 0x04034b50)) throw new Error('Excel ZIP 文件头无效。');
  if (data.byteLength < 4 || view.getUint32(0, true) !== 0x04034b50) return [];
  let end = -1;
  for (let i = data.byteLength - 4; i >= Math.max(0, data.byteLength - 65_557); i--) {
    if (view.getUint32(i, true) !== 0x06054b50) continue;
    // SheetJS chooses the last signature, so reject deceptive comments rather
    // than validating one directory and allowing the parser to use another.
    if (i + 22 > data.byteLength || i + 22 + view.getUint16(i + 20, true) !== data.byteLength) throw new Error('Excel 压缩目录尾部无效。');
    end = i; break;
  }
  if (end < 0) throw new Error('Excel 压缩目录不完整，请下载原件核对。');
  const count = view.getUint16(end + 10, true);
  if (view.getUint16(end + 4, true) !== 0 || view.getUint16(end + 6, true) !== 0 || view.getUint16(end + 8, true) !== count) throw new Error('暂不支持分卷或无效的 Excel 压缩目录。');
  let offset = view.getUint32(end + 16, true), expanded = 0;
  const directoryStart = offset, members: ZipMember[] = [], names = new Set<string>();
  if (count === 0xffff || offset === 0xffffffff || count > EXCEL_PREVIEW_LIMITS.entries) throw new Error('文件结构过大，请下载原件查看。');
  const checkExtra = (start: number, length: number) => {
    const last = start + length;
    if (last > data.byteLength) throw new Error('Excel 压缩目录无效。');
    for (let at = start; at < last;) {
      if (at + 4 > last || view.getUint16(at, true) === 1) throw new Error('暂不预览 ZIP64 或无效扩展结构，请下载原件查看。');
      at += 4 + view.getUint16(at + 2, true);
      if (at > last) throw new Error('Excel 扩展结构无效。');
    }
  };
  for (let i = 0; i < count; i++) {
    if (offset + 46 > end || view.getUint32(offset, true) !== 0x02014b50) throw new Error('Excel 压缩目录无效。');
    const size = view.getUint32(offset + 24, true);
    expanded += size;
    if (size > EXCEL_PREVIEW_LIMITS.entryBytes || expanded > expandedLimit) throw new Error('文件展开后过大，已停止预览以保护浏览器。请下载原件查看。');
    const compressed = view.getUint32(offset + 20, true), local = view.getUint32(offset + 42, true);
    const flags = view.getUint16(offset + 8, true), method = view.getUint16(offset + 10, true);
    const nameLength = view.getUint16(offset + 28, true), extraLength = view.getUint16(offset + 30, true);
    if (offset + 46 + nameLength + extraLength + view.getUint16(offset + 32, true) > end) throw new Error('Excel 压缩目录无效。');
    const name = new TextDecoder('utf-8', { fatal: true }).decode(new Uint8Array(data, offset + 46, nameLength));
    if (!name || name.startsWith('/') || /[\\\x00-\x1f:]/.test(name) || name.split('/').some(part => part === '..' || part === '.') || names.has(name)) throw new Error('Excel 压缩成员路径无效或重复。');
    names.add(name);
    checkExtra(offset + 46 + nameLength, extraLength);
    if (local + 30 > directoryStart || view.getUint32(local, true) !== 0x04034b50 || (flags & 0x2041) || ![0, 8].includes(method)) throw new Error('Excel 压缩文件头无效或不受支持。');
    const localFlags = view.getUint16(local + 6, true), localMethod = view.getUint16(local + 8, true);
    const localCompressed = view.getUint32(local + 18, true), localExpanded = view.getUint32(local + 22, true);
    const localName = view.getUint16(local + 26, true), localExtra = view.getUint16(local + 28, true);
    const start = local + 30 + localName + localExtra;
    checkExtra(local + 30 + localName, localExtra);
    if (localFlags !== flags || localMethod !== method || localName !== nameLength || start + compressed > directoryStart ||
      (localCompressed !== compressed && !((flags & 8) && localCompressed === 0)) ||
      (localExpanded !== size && !((flags & 8) && localExpanded === 0)) || (method === 0 && compressed !== size)) throw new Error('Excel 压缩文件头与目录不一致。');
    for (let index = 0; index < nameLength; index++) {
      if (view.getUint8(local + 30 + index) !== view.getUint8(offset + 46 + index)) throw new Error('Excel 压缩文件名与目录不一致。');
    }
    members.push({ name, start, compressed, expanded: size, method });
    offset += 46 + nameLength + extraLength + view.getUint16(offset + 32, true);
  }
  if (offset !== end || directoryStart + view.getUint32(end + 12, true) !== end) throw new Error('Excel 压缩目录无效。');
  return members;
}

export function validateExcelArchive(data: ArrayBuffer): void { inspectExcelArchive(data); }

/** Validate the actual deflate output before the library can allocate from it. */
export async function validateExcelInflation(data: ArrayBuffer): Promise<void> {
  for (const member of inspectExcelArchive(data)) {
    if (member.method !== 8) continue;
    const compressed = new Blob([new Uint8Array(data, member.start, member.compressed)]);
    const reader = compressed.stream().pipeThrough(new DecompressionStream('deflate-raw')).getReader();
    let expanded = 0;
    try {
      while (true) {
        const chunk = await reader.read();
        if (chunk.done) break;
        expanded += chunk.value.byteLength;
        if (expanded > member.expanded || expanded > EXCEL_PREVIEW_LIMITS.entryBytes) throw new Error('文件展开大小与声明不符，已停止预览。');
      }
      if (expanded !== member.expanded) throw new Error('Excel 压缩内容不完整。');
    } finally { await reader.cancel().catch(() => {}); }
  }
}

function decodeText(data: ArrayBuffer): string {
  try { return new TextDecoder('utf-8', { fatal: true }).decode(data); }
  catch { return new TextDecoder('gbk').decode(data); }
}

type PreviewBudget = { left: number };
async function readMember(data: ArrayBuffer, member: ZipMember, budget: PreviewBudget): Promise<Uint8Array> {
  if (member.expanded > budget.left) throw new Error('当前工作表及公共信息展开后过大，请下载原件查看。');
  if (member.method === 0) {
    budget.left -= member.expanded;
    return new Uint8Array(data, member.start, member.compressed);
  }
  const compressed = new Blob([new Uint8Array(data, member.start, member.compressed)]);
  const reader = compressed.stream().pipeThrough(new DecompressionStream('deflate-raw')).getReader();
  const result = new Uint8Array(member.expanded);
  let expanded = 0;
  try {
    while (true) {
      const chunk = await reader.read();
      if (chunk.done) break;
      expanded += chunk.value.byteLength;
      if (expanded > member.expanded || expanded > budget.left) throw new Error('文件展开大小与声明不符，已停止预览。');
      result.set(chunk.value, expanded - chunk.value.byteLength);
    }
    if (expanded !== member.expanded) throw new Error('Excel 压缩内容不完整。');
    budget.left -= expanded;
    return result;
  } finally { await reader.cancel().catch(() => {}); }
}

/** Validate actual output while scanning large worksheet XML without a DOM. */
async function scanMemberXml(data: ArrayBuffer, member: ZipMember, scanner: ReturnType<typeof sheetDrawingScanner>): Promise<void> {
  if (member.expanded > EXCEL_PREVIEW_LIMITS.entryBytes) throw new Error('当前工作表 XML 超过预览上限。');
  let expanded = 0, decoder: TextDecoder | undefined;
  const chunk = (bytes: Uint8Array) => {
    expanded += bytes.byteLength;
    if (expanded > member.expanded || expanded > EXCEL_PREVIEW_LIMITS.entryBytes) throw new Error('文件展开大小与声明不符，已停止图表预览。');
    if (!decoder) {
      let encoding = 'utf-8';
      if ((bytes[0] === 0xff && bytes[1] === 0xfe) || (bytes[0] === 0x3c && bytes[1] === 0)) encoding = 'utf-16le';
      if ((bytes[0] === 0xfe && bytes[1] === 0xff) || (bytes[0] === 0 && bytes[1] === 0x3c)) encoding = 'utf-16be';
      decoder = new TextDecoder(encoding, { fatal: true });
    }
    scanner.write(decoder.decode(bytes, { stream: true }));
  };
  if (member.method === 0) {
    for (let at = 0; at < member.compressed; at += 65_536) chunk(new Uint8Array(data, member.start + at, Math.min(65_536, member.compressed - at)));
  } else {
    const reader = new Blob([new Uint8Array(data, member.start, member.compressed)]).stream()
      .pipeThrough(new DecompressionStream('deflate-raw')).getReader();
    try { for (;;) { const item = await reader.read(); if (item.done) break; chunk(item.value); } }
    finally { await reader.cancel().catch(() => {}); }
  }
  if (expanded !== member.expanded) throw new Error('Excel 图表所属工作表压缩内容不完整。');
  scanner.write(decoder?.decode() || '').close();
}

function xmlText(bytes: Uint8Array): string {
  let encoding = 'utf-8';
  if ((bytes[0] === 0xff && bytes[1] === 0xfe) || (bytes[0] === 0x3c && bytes[1] === 0)) encoding = 'utf-16le';
  if ((bytes[0] === 0xfe && bytes[1] === 0xff) || (bytes[0] === 0 && bytes[1] === 0x3c)) encoding = 'utf-16be';
  const xml = new TextDecoder(encoding, { fatal: true }).decode(bytes);
  if (/<!DOCTYPE|<!ENTITY/i.test(xml)) throw new Error('Excel 元数据包含不支持的 XML 声明。');
  return xml.replace(/\r\n?|\u0085|\u2028/g, '\n');
}

function xmlRoot(xml: string): XmlElement {
  const root = new DOMParser({ onError() { throw new Error('Excel XML 元数据无效。'); } })
    .parseFromString(xml, 'application/xml').documentElement;
  if (!root) throw new Error('Excel XML 元数据为空。');
  return root;
}

function directChildren(parent: XmlElement, name: string, namespace: string): XmlElement[] {
  const children: XmlElement[] = [];
  for (let node = parent.firstChild; node; node = node.nextSibling) {
    if (node.nodeType !== 1) continue;
    const element = node as XmlElement;
    if (element.localName === name && element.namespaceURI === namespace) children.push(element);
  }
  if (children.length > EXCEL_PREVIEW_LIMITS.entries) throw new Error('Excel 元数据条目过多。');
  return children;
}

const RELATIONSHIP_NAMESPACES = new Set([
  'http://schemas.openxmlformats.org/officeDocument/2006/relationships',
  'http://purl.oclc.org/ooxml/officeDocument/relationships',
]);
const WORKBOOK_NAMESPACES = new Set([
  'http://schemas.openxmlformats.org/spreadsheetml/2006/main',
  'http://purl.oclc.org/ooxml/spreadsheetml/main',
]);
const PACKAGE_NAMESPACES = new Set([
  'http://schemas.openxmlformats.org/package/2006/relationships',
  'http://purl.oclc.org/ooxml/package/relationships',
]);

function sheetRelationshipId(item: XmlElement, xml: string, lines: number[]): string {
  // xmldom normalizes duplicate expanded attribute names while creating the
  // DOM. Check the original start tag too, so conflicting namespace aliases
  // cannot disappear before the ID validation below.
  const located = item as XmlElement & { lineNumber?: number; columnNumber?: number };
  const start = (lines[(located.lineNumber || 0) - 1] ?? -1) + (located.columnNumber || 0) - 1;
  if (start < 0 || xml[start] !== '<') throw new Error('Excel 工作表来源位置无效。');
  let end = start + 1, quote = '';
  for (; end < xml.length; end++) {
    const character = xml[end]!;
    if (quote) { if (character === quote) quote = ''; }
    else if (character === '"' || character === "'") quote = character;
    else if (character === '>') break;
  }
  let count = 0;
  for (const match of xml.slice(start, end + 1).matchAll(/([A-Za-z_][\w.:-]*)\s*=\s*("[^"]*"|'[^']*')/g)) {
    const parts = match[1]!.split(':');
    if (parts.length === 2 && parts[1] === 'id' && RELATIONSHIP_NAMESPACES.has(item.lookupNamespaceURI(parts[0]!) || '')) count++;
  }
  if (count !== 1) throw new Error('Excel 工作表来源关系无效或矛盾。');
  const ids = new Set<string>();
  for (let index = 0; index < item.attributes.length; index++) {
    const attribute = item.attributes.item(index)!;
    if (attribute.localName === 'id' && RELATIONSHIP_NAMESPACES.has(attribute.namespaceURI || '')) ids.add(attribute.value);
  }
  if (ids.size !== 1) throw new Error('Excel 工作表来源关系无效或矛盾。');
  return [...ids][0]!;
}

type PartRelationship = { type: string; target: string; external: boolean };
function relationships(xml: string): Map<string, PartRelationship> {
  const root = xmlRoot(xml);
  if (root.localName !== 'Relationships' || !PACKAGE_NAMESPACES.has(root.namespaceURI || '')) throw new Error('Excel 来源关系命名空间无效。');
  const result = new Map<string, PartRelationship>();
  for (const item of directChildren(root, 'Relationship', root.namespaceURI!)) {
    const id = item.getAttribute('Id'), type = item.getAttribute('Type'), target = item.getAttribute('Target');
    const mode = item.getAttribute('TargetMode');
    if (!id || !type || !target || result.has(id)) throw new Error('Excel 来源关系无效或重复。');
    result.set(id, { type, target, external: !!mode && mode !== 'Internal' });
  }
  return result;
}

function resolvePart(base: string, relationship: PartRelationship, members: Map<string, ZipMember>): string {
  if (relationship.external) throw new Error('Excel 工作表或公共信息不能使用外部来源关系。');
  let decoded: string, decodedBase: string;
  try { decoded = decodeURIComponent(relationship.target); decodedBase = decodeURIComponent(base); }
  catch { throw new Error('Excel 来源路径编码无效。'); }
  const normalize = (parent: string, target: string) => {
    if (!target || /[\\\x00-\x1f:#?]/.test(target) || target.startsWith('//')) throw new Error('Excel 来源路径无效。');
    const parts = target.startsWith('/') ? [] : parent.split('/').slice(0, -1);
    for (const part of target.split('/')) {
      if (!part || part === '.') continue;
      if (part === '..') { if (!parts.length) throw new Error('Excel 来源路径超出文件范围。'); parts.pop(); }
      else parts.push(part);
    }
    if (!parts.length) throw new Error('Excel 来源路径无效。');
    return parts.join('/');
  };
  normalize(decodedBase, decoded);
  const exact = normalize(base, relationship.target), alternative = normalize(base, decoded);
  if (exact !== alternative && members.has(exact) && members.has(alternative)) throw new Error('Excel 来源路径存在编码歧义，未替换工作表。');
  return members.has(exact) ? exact : alternative;
}

function relationshipPart(part: string): string {
  const pieces = part.split('/'), name = pieces.pop()!;
  return [...pieces, '_rels', name + '.rels'].join('/');
}
function escapedXml(value: string): string {
  return value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;' })[character]!);
}

/** Conservative retained DTO estimate, including sparse-array slots and text. */
function retainedChartBytes(value: unknown, limit = EXCEL_PREVIEW_LIMITS.chartCacheBytes): number {
  if (typeof value === 'string') return 64 + value.length * 2;
  if (value === null || typeof value !== 'object') return 32;
  let bytes = Array.isArray(value) ? 64 + value.length * 8 : 256;
  if (bytes > limit) return bytes;
  if (Array.isArray(value)) {
    for (const item of value) { bytes += retainedChartBytes(item, limit - bytes); if (bytes > limit) break; }
  } else {
    for (const [key, item] of Object.entries(value)) {
      bytes += 64 + key.length * 2 + retainedChartBytes(item, limit - bytes);
      if (bytes > limit) break;
    }
  }
  return bytes;
}

/** The full ZIP stays compressed; SheetJS receives only one bounded sheet. */
export class ExcelPreviewArchive {
  sheetNames: string[] = [];
  private members: Map<string, ZipMember>;
  private sheets = new Map<string, { target: string; kind: string }>();
  private publicParts = new Map<'sharedStrings' | 'styles', string>();
  private publicBytes = new Map<string, Uint8Array>();
  private date1904 = false;
  private selectedSheet = '';
  private selected: ExcelPreviewWorkbook | null = null;
  private fallback: ExcelPreviewWorkbook | null = null;
  private chartSheet = '';
  private chartDirectory: ExcelPreviewChartList | null = null;
  private chartCache = new Map<string, { chart: ExcelPreviewChart; bytes: number }>();
  private chartCacheBytes = 0;

  private constructor(private data: ArrayBuffer, members: ZipMember[]) {
    this.members = new Map(members.map(member => [member.name, member]));
  }

  static async open(data: ArrayBuffer, extension: string): Promise<ExcelPreviewArchive> {
    const members = inspectExcelArchive(data, EXCEL_PREVIEW_LIMITS.archiveExpandedBytes);
    const archive = new ExcelPreviewArchive(data, members);
    if (extension.toLowerCase() === 'xlsx' && !members.length) throw new Error('XLSX 必须是有效的 Excel ZIP 工作簿。');
    if (extension.toLowerCase() !== 'xlsx') {
      await validateExcelInflation(data);
      archive.fallback = new ExcelPreviewWorkbook(data, extension);
      archive.sheetNames = archive.fallback.sheetNames;
      return archive;
    }
    await archive.readMetadata();
    return archive;
  }

  private member(name: string): ZipMember {
    const member = this.members.get(name);
    if (!member || name.endsWith('/')) throw new Error('Excel 工作表或公共来源不存在。');
    return member;
  }

  private async readMetadata(): Promise<void> {
    this.member('[Content_Types].xml');
    const budget = { left: EXCEL_PREVIEW_LIMITS.metadataBytes };
    const readXml = async (part: string) => xmlText(await readMember(this.data, this.member(part), budget));
    const root = relationships(await readXml('_rels/.rels'));
    const roots = [...root.values()].filter(item => item.type.endsWith('/officeDocument'));
    if (roots.length !== 1) throw new Error('Excel 工作簿来源关系无效。');
    const workbookPart = resolvePart('', roots[0]!, this.members);
    const workbookXml = await readXml(workbookPart);
    const workbook = xmlRoot(workbookXml), namespace = workbook.namespaceURI || '';
    if (workbook.localName !== 'workbook' || !WORKBOOK_NAMESPACES.has(namespace)) throw new Error('Excel 工作簿 XML 命名空间无效。');
    const containers = directChildren(workbook, 'sheets', namespace);
    if (containers.length !== 1) throw new Error('Excel 工作表目录结构无效。');
    const rels = relationships(await readXml(relationshipPart(workbookPart)));
    const lines = [0];
    for (let index = 0; index < workbookXml.length; index++) if (workbookXml[index] === '\n') lines.push(index + 1);
    const names = new Set<string>();
    for (const item of directChildren(containers[0]!, 'sheet', namespace)) {
      const relation = rels.get(sheetRelationshipId(item, workbookXml, lines));
      const name = item.getAttribute('name');
      const kind = relation?.type.split('/').at(-1) || '';
      if (!name || name.length > 1024 || names.has(name) || !relation
          || !['worksheet', 'chartsheet', 'dialogsheet', 'macrosheet', 'intlMacrosheet'].includes(kind)) throw new Error('Excel 工作表关系无效。');
      names.add(name);
      if (kind !== 'worksheet') continue;
      const target = resolvePart(workbookPart, relation, this.members);
      this.member(target);
      this.sheets.set(name, { target, kind });
    }
    this.sheetNames = [...this.sheets.keys()];
    if (!this.sheetNames.length) throw new Error('没有可预览的数据工作表；图表或宏页面请下载原件查看。');
    for (const kind of ['sharedStrings', 'styles'] as const) {
      const candidates = [...rels.values()].filter(item => item.type.endsWith('/' + kind));
      if (candidates.length > 1) throw new Error('Excel 公共来源关系重复。');
      if (candidates[0]) {
        const target = resolvePart(workbookPart, candidates[0], this.members);
        this.member(target); this.publicParts.set(kind, target);
      }
    }
    const properties = directChildren(workbook, 'workbookPr', namespace);
    if (properties.length > 1) throw new Error('Excel 工作簿属性重复。');
    const date1904 = properties[0]?.getAttribute('date1904');
    this.date1904 = date1904 === '1' || date1904 === 'true';
  }

  async chartList(sheet: string): Promise<ExcelPreviewChartList> {
    if (this.fallback) return { charts: [], notices: ['此格式暂不支持原生图表预览。'] };
    const source = this.sheets.get(sheet);
    if (!source) throw new Error('工作表不存在。');
    if (this.chartSheet === sheet && this.chartDirectory) return this.chartDirectory;
    this.chartCache.clear(); this.chartCacheBytes = 0;
    this.chartSheet = ''; this.chartDirectory = null;
    const budget = { left: EXCEL_CHART_LIMITS.metadataBytes };
    const readXml = async (part: string) => {
      const member = this.member(part), limit = part.endsWith('.rels') ? 512 * 1024 : 2 * 1024 * 1024;
      if (member.expanded > limit) throw new Error('当前工作表的图表关系或绘图目录过大，已停止预览。');
      return xmlText(await readMember(this.data, member, budget));
    };
    const sheetRelsPart = relationshipPart(source.target);
    if (!this.members.has(sheetRelsPart)) return { charts: [] };
    const links = relationships(await readXml(sheetRelsPart));
    const typeIs = (link: PartRelationship, kind: string) => [...RELATIONSHIP_NAMESPACES].some(base => link.type === `${base}/${kind}`);
    // No worksheet inflate is needed if no drawing relationship exists.
    if (![...links.values()].some(link => typeIs(link, 'drawing'))) return { charts: [] };
    const drawingIds: string[] = [];
    await scanMemberXml(this.data, this.member(source.target), sheetDrawingScanner(drawingIds));
    const result: ExcelPreviewChartList = { charts: [] }, seen = new Set<string>();
    for (const id of drawingIds) {
      const drawingLink = links.get(id);
      if (!drawingLink || !typeIs(drawingLink, 'drawing')) throw new Error('Excel 图形关系类型或来源无效。');
      const drawingPart = resolvePart(source.target, drawingLink, this.members);
      const relsPart = relationshipPart(drawingPart);
      const drawingXml = await readXml(drawingPart);
      const entries = parseDrawingCharts(drawingXml);
      if (!entries.length) continue;
      if (!this.members.has(relsPart)) throw new Error('Excel 图表来源关系缺失。');
      const chartLinks = relationships(await readXml(relsPart));
      for (const entry of entries) {
        const link = chartLinks.get(entry.relationshipId);
        if (!link || !typeIs(link, 'chart')) throw new Error('Excel 图表关系类型或来源无效。');
        const target = resolvePart(drawingPart, link, this.members);
        this.member(target);
        if (seen.has(target)) continue;
        seen.add(target);
        if (result.charts.length >= EXCEL_CHART_LIMITS.charts) throw new Error('当前工作表图表目录超过预览上限。');
        result.charts.push({ id: target, title: `图表 ${result.charts.length + 1}`, ...('anchorRow' in entry ? { anchorRow: entry.anchorRow } : {}) });
      }
    }
    this.chartSheet = sheet; this.chartDirectory = result; return result;
  }

  async chart(sheet: string, id: string): Promise<ExcelPreviewChart> {
    // A chart view does not retain the full selected-sheet grid as well.
    // The immutable original remains available to rebuild it on returning.
    this.selected = null; this.selectedSheet = '';
    const list = await this.chartList(sheet), entry = list.charts.find(item => item.id === id);
    if (!entry) throw new Error('图表不属于当前工作表，不能读取。');
    // Membership is checked before every hit, even though this cache is local
    // to one immutable Archive/Blob and is cleared whenever its sheet changes.
    const cached = this.chartCache.get(id);
    if (cached) {
      this.chartCache.delete(id); this.chartCache.set(id, cached);
      return cached.chart;
    }
    const budget = { left: EXCEL_CHART_LIMITS.chartBytes };
    const bytes = await readMember(this.data, this.member(id), budget);
    const chart = parseExcelChart(xmlText(bytes), id, this.date1904);
    entry.title = chart.title;
    const retained = retainedChartBytes(chart);
    if (this.chartSheet === sheet && retained <= EXCEL_PREVIEW_LIMITS.chartCacheBytes) {
      // Two in-flight requests may finish for the same ID; replacing that
      // entry must not count its retained bytes twice. A stale other-sheet
      // completion is returned to its caller but never fills the new cache.
      const prior = this.chartCache.get(id);
      if (prior) { this.chartCacheBytes -= prior.bytes; this.chartCache.delete(id); }
      while (this.chartCache.size >= EXCEL_PREVIEW_LIMITS.chartCacheEntries
          || this.chartCacheBytes + retained > EXCEL_PREVIEW_LIMITS.chartCacheBytes) {
        const oldest = this.chartCache.keys().next().value;
        if (oldest === undefined) break;
        this.chartCacheBytes -= this.chartCache.get(oldest)!.bytes; this.chartCache.delete(oldest);
      }
      this.chartCache.set(id, { chart, bytes: retained }); this.chartCacheBytes += retained;
    }
    return chart;
  }

  async page(sheet: string, row = 0, column = 0): Promise<ExcelPreviewPage> {
    if (this.fallback) return this.fallback.page(sheet, row, column);
    if (!this.sheets.has(sheet)) throw new Error('工作表不存在。');
    if (this.chartSheet && this.chartSheet !== sheet) {
      this.chartCache.clear(); this.chartCacheBytes = 0; this.chartDirectory = null; this.chartSheet = '';
    }
    if (this.sheets.get(sheet)!.kind !== 'worksheet') throw new Error('该标签为图表或宏页面，请下载原件查看；其他工作表仍可预览。');
    if (sheet !== this.selectedSheet) {
      this.selected = null; this.selectedSheet = '';
      const budget = { left: EXCEL_PREVIEW_LIMITS.expandedBytes };
      const zip = new JSZip();
      const relationNamespace = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
      const packageNamespace = 'http://schemas.openxmlformats.org/package/2006/relationships';
      const contentNamespace = 'http://schemas.openxmlformats.org/package/2006/content-types';
      const contentType = 'application/vnd.openxmlformats-officedocument.spreadsheetml.';
      const overrides = [
        `<Override PartName="/xl/workbook.xml" ContentType="${contentType}sheet.main+xml"/>`,
        `<Override PartName="/xl/worksheets/selected.xml" ContentType="${contentType}worksheet+xml"/>`,
      ];
      const links = ['<Relationship Id="selected" Type="' + relationNamespace + '/worksheet" Target="worksheets/selected.xml"/>'];
      for (const [kind, target] of this.publicParts) {
        let bytes = this.publicBytes.get(target);
        if (bytes) {
          if (bytes.byteLength > budget.left) throw new Error('当前工作表的公共信息过大，请下载原件查看。');
          budget.left -= bytes.byteLength;
        } else {
          bytes = await readMember(this.data, this.member(target), budget);
          this.publicBytes.set(target, bytes);
        }
        zip.file('xl/' + kind + '.xml', bytes);
        overrides.push(`<Override PartName="/xl/${kind}.xml" ContentType="${contentType}${kind}+xml"/>`);
        links.push(`<Relationship Id="${kind}" Type="${relationNamespace}/${kind}" Target="${kind}.xml"/>`);
      }
      zip.file('xl/worksheets/selected.xml', await readMember(this.data, this.member(this.sheets.get(sheet)!.target), budget));
      zip.file('[Content_Types].xml', `<Types xmlns="${contentNamespace}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>${overrides.join('')}</Types>`);
      zip.file('_rels/.rels', `<Relationships xmlns="${packageNamespace}"><Relationship Id="workbook" Type="${relationNamespace}/officeDocument" Target="xl/workbook.xml"/></Relationships>`);
      zip.file('xl/workbook.xml', `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="${relationNamespace}"><workbookPr date1904="${this.date1904 ? 1 : 0}"/><sheets><sheet name="${escapedXml(sheet)}" sheetId="1" r:id="selected"/></sheets></workbook>`);
      zip.file('xl/_rels/workbook.xml.rels', `<Relationships xmlns="${packageNamespace}">${links.join('')}</Relationships>`);
      const bounded = await zip.generateAsync({ type: 'arraybuffer', compression: 'STORE' });
      this.selected = new ExcelPreviewWorkbook(bounded, 'xlsx');
      this.selectedSheet = sheet;
    }
    return this.selected!.page(sheet, row, column);
  }
}

export class ExcelPreviewWorkbook {
  readonly sheetNames: string[];
  private source: ArrayBuffer | string;
  private inputOptions: XLSX.ParsingOptions;
  private selectedSheet = '';
  private selected: XLSX.WorkSheet | undefined;

  constructor(data: ArrayBuffer, extension: string) {
    validateExcelArchive(data);
    const text = ['csv', 'tsv', 'tab'].includes(extension.toLowerCase());
    this.source = text ? decodeText(data) : data;
    this.inputOptions = {
      type: text ? 'string' : 'array',
      ...(extension === 'tsv' || extension === 'tab' ? { FS: '\t' } : {}),
      dense: false, cellHTML: false, cellFormula: false, cellStyles: false,
      bookVBA: false, bookFiles: false, bookDeps: false,
    };
    this.sheetNames = XLSX.read(this.source, { ...this.inputOptions, bookSheets: true, sheetRows: 1 }).SheetNames;
    if (!this.sheetNames.length) throw new Error('没有可预览的工作表。');
  }

  page(sheet: string, requestedRow = 0, requestedColumn = 0): ExcelPreviewPage {
    if (!this.sheetNames.includes(sheet)) throw new Error('工作表不存在。');
    if (sheet !== this.selectedSheet) {
      this.selected = undefined;
      this.selectedSheet = '';
      const workbook = XLSX.read(this.source, { ...this.inputOptions, sheets: sheet, sheetRows: EXCEL_PREVIEW_LIMITS.rows });
      this.selected = workbook.Sheets[sheet];
      this.selectedSheet = sheet;
    }
    const worksheet = this.selected || {};
    const range = XLSX.utils.decode_range(worksheet['!fullref'] || worksheet['!ref'] || 'A1');
    const totalRows = Math.max(1, Math.min(1_048_576, range.e.r + 1));
    const totalColumns = Math.max(1, Math.min(16_384, range.e.c + 1));
    const previewRows = Math.min(totalRows, EXCEL_PREVIEW_LIMITS.rows);
    const index = (value: number, max: number, size: number) => Math.floor(Math.min(max - 1, Math.max(0, Number.isFinite(value) ? value : 0)) / size) * size;
    const startRow = index(requestedRow, previewRows, EXCEL_PREVIEW_LIMITS.pageRows);
    const startColumn = index(requestedColumn, totalColumns, EXCEL_PREVIEW_LIMITS.pageColumns);
    const rowEnd = Math.min(previewRows, startRow + EXCEL_PREVIEW_LIMITS.pageRows);
    const columnEnd = Math.min(totalColumns, startColumn + EXCEL_PREVIEW_LIMITS.pageColumns);
    const merges = (worksheet['!merges'] || []).filter(m => m.s.r < rowEnd && m.e.r >= startRow && m.s.c < columnEnd && m.e.c >= startColumn).slice(0, 10_000);
    const rows: ExcelPreviewPage['rows'] = [];
    for (let row = startRow; row < rowEnd; row++) {
      const cells: ExcelPreviewCell[] = [];
      for (let column = startColumn; column < columnEnd; column++) {
        const merge = merges.find(m => row >= m.s.r && row <= m.e.r && column >= m.s.c && column <= m.e.c);
        const visibleRow = merge ? Math.max(startRow, merge.s.r) : row;
        const visibleColumn = merge ? Math.max(startColumn, merge.s.c) : column;
        const cell = worksheet[XLSX.utils.encode_cell(merge ? merge.s : { r: row, c: column })];
        cells.push({
          // Vue renders text, never the file's HTML, hyperlink or formula.
          text: cell ? String(cell.w ?? (cell.v == null ? '' : XLSX.utils.format_cell(cell))).slice(0, EXCEL_PREVIEW_LIMITS.cellCharacters) : '',
          rowSpan: merge ? Math.min(rowEnd - 1, merge.e.r) - visibleRow + 1 : 1,
          colSpan: merge ? Math.min(columnEnd - 1, merge.e.c) - visibleColumn + 1 : 1,
          hidden: !!merge && (row !== visibleRow || column !== visibleColumn),
        });
      }
      rows.push({ number: row + 1, cells });
    }
    return { sheet, startRow, startColumn, totalRows, previewRows, totalColumns,
      columns: Array.from({ length: columnEnd - startColumn }, (_, i) => XLSX.utils.encode_col(startColumn + i)), rows,
      truncated: totalRows > previewRows };
  }
}
