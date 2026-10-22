import * as XLSX from 'xlsx';

// Preview is deliberately separate from structured import. It never evaluates
// formulas, loads remote links or publishes data, and keeps only one sheet.
export const EXCEL_PREVIEW_LIMITS = Object.freeze({
  fileBytes: 100 * 1024 * 1024,
  expandedBytes: 128 * 1024 * 1024,
  entryBytes: 64 * 1024 * 1024,
  entries: 20_000,
  rows: 5_000,
  pageRows: 100,
  pageColumns: 30,
  cellCharacters: 4_096,
});

export type ExcelPreviewCell = { text: string; rowSpan: number; colSpan: number; hidden: boolean };
export type ExcelPreviewPage = {
  sheet: string; startRow: number; startColumn: number;
  totalRows: number; previewRows: number; totalColumns: number;
  columns: string[]; rows: { number: number; cells: ExcelPreviewCell[] }[];
  truncated: boolean;
};

/** Inspect ZIP metadata before SheetJS inflates it. No archive extraction here. */
type ZipMember = { start: number; compressed: number; expanded: number; method: number };
function inspectExcelArchive(data: ArrayBuffer): ZipMember[] {
  if (data.byteLength > EXCEL_PREVIEW_LIMITS.fileBytes) throw new Error('文件超过 100 MB，请下载原件查看。');
  const view = new DataView(data);
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
  const directoryStart = offset, members: ZipMember[] = [];
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
    if (size > EXCEL_PREVIEW_LIMITS.entryBytes || expanded > EXCEL_PREVIEW_LIMITS.expandedBytes) throw new Error('文件展开后过大，已停止预览以保护浏览器。请下载原件查看。');
    const compressed = view.getUint32(offset + 20, true), local = view.getUint32(offset + 42, true);
    const flags = view.getUint16(offset + 8, true), method = view.getUint16(offset + 10, true);
    const nameLength = view.getUint16(offset + 28, true), extraLength = view.getUint16(offset + 30, true);
    checkExtra(offset + 46 + nameLength, extraLength);
    if (local + 30 > directoryStart || view.getUint32(local, true) !== 0x04034b50 || (flags & 0x2041) || ![0, 8].includes(method)) throw new Error('Excel 压缩文件头无效或不受支持。');
    const localFlags = view.getUint16(local + 6, true), localMethod = view.getUint16(local + 8, true);
    const localCompressed = view.getUint32(local + 18, true), localExpanded = view.getUint32(local + 22, true);
    const localName = view.getUint16(local + 26, true), localExtra = view.getUint16(local + 28, true);
    const start = local + 30 + localName + localExtra;
    checkExtra(local + 30 + localName, localExtra);
    if (localFlags !== flags || localMethod !== method || start + compressed > directoryStart ||
      (localCompressed !== compressed && !((flags & 8) && localCompressed === 0)) ||
      (localExpanded !== size && !((flags & 8) && localExpanded === 0)) || (method === 0 && compressed !== size)) throw new Error('Excel 压缩文件头与目录不一致。');
    members.push({ start, compressed, expanded: size, method });
    offset += 46 + nameLength + extraLength + view.getUint16(offset + 32, true);
  }
  if (offset > end) throw new Error('Excel 压缩目录无效。');
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
