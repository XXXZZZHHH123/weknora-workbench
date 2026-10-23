import saxModule, { type QualifiedTag, type SAXParser } from 'sax';
import * as XLSX from 'xlsx';

export type ExcelPreviewChartList = { charts: { id: string; title: string; anchorRow?: number }[]; notices?: string[] };
export type ExcelPreviewChart = {
  id: string; title: string;
  series: { name: string; type: 'line' | 'bar' | 'area'; values: (number | null)[];
    categories: (string | number | null)[]; color?: string; axisId?: string; categoryAxisId?: string;
    grouping?: 'standard' | 'stacked' | 'percentStacked'; smooth?: boolean; horizontal?: boolean; groupId?: string;
    categoryTimes?: (number | null)[] }[];
  axes: { id: string; type: 'category' | 'value' | 'date'; position?: 'left' | 'right' | 'top' | 'bottom';
    title?: string; min?: number; max?: number; numFmt?: string; crossAxisId?: string; inverse?: boolean }[];
  notices: string[];
};
export const EXCEL_CHART_LIMITS = Object.freeze({ chartBytes: 4 * 1024 * 1024,
  metadataBytes: 8 * 1024 * 1024, nodes: 200_000, points: 100_000, series: 256, charts: 2_000 });

export const CHART_NAMESPACES = new Set(['http://schemas.openxmlformats.org/drawingml/2006/chart',
  'http://purl.oclc.org/ooxml/drawingml/chart']);
export const DRAWING_NAMESPACES = new Set(['http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing',
  'http://purl.oclc.org/ooxml/drawingml/spreadsheetDrawing']);
export const SHEET_NAMESPACES = new Set(['http://schemas.openxmlformats.org/spreadsheetml/2006/main',
  'http://purl.oclc.org/ooxml/spreadsheetml/main']);
const REL_NAMESPACES = new Set(['http://schemas.openxmlformats.org/officeDocument/2006/relationships',
  'http://purl.oclc.org/ooxml/officeDocument/relationships']);
const TEXT_NAMESPACES = new Set(['http://schemas.openxmlformats.org/drawingml/2006/main',
  'http://purl.oclc.org/ooxml/drawingml/main']);

/** Strict namespace-aware streaming parser; never loads entities or remote XML. */
export function chartXmlScanner(onOpen: (tag: QualifiedTag, parents: QualifiedTag[]) => void,
  onClose?: (tag: QualifiedTag) => void, onText?: (text: string) => void): SAXParser {
  const options = { xmlns: true, strictEntities: true, trim: false };
  const sax = saxModule.parser(true, options);
  const stack: QualifiedTag[] = [];
  sax.onerror = () => { throw new Error('Excel 图表 XML 无效或包含非法实体。'); };
  sax.ondoctype = () => { throw new Error('Excel 图表不允许 DTD 或实体声明。'); };
  sax.onopentag = raw => {
    const tag = raw as QualifiedTag;
    if (stack.length >= 128) throw new Error('Excel 图表 XML 层级过深。');
    const attrs = new Set<string>();
    for (const value of Object.values(tag.attributes)) {
      if (typeof value === 'string') continue;
      const key = value.uri + ':' + value.local;
      if (attrs.has(key)) throw new Error('Excel 图表 XML 属性存在命名空间歧义。');
      attrs.add(key);
    }
    onOpen(tag, stack); stack.push(tag);
  };
  sax.onclosetag = () => { const tag = stack.pop(); if (tag) onClose?.(tag); };
  sax.ontext = text => onText?.(text);
  sax.oncdata = text => onText?.(text);
  return sax;
}
function attr(tag: QualifiedTag, name: string): string | undefined {
  const value = tag.attributes[name];
  return typeof value === 'string' ? value : value?.value;
}
export function chartRelationId(tag: QualifiedTag): string {
  const ids = Object.values(tag.attributes).filter(value => typeof value !== 'string'
    && value.local === 'id' && REL_NAMESPACES.has(value.uri));
  if (ids.length !== 1 || typeof ids[0] === 'string' || !ids[0]?.value) throw new Error('Excel 图表来源关系无效。');
  return ids[0].value;
}
export function sheetDrawingScanner(ids: string[]): SAXParser {
  let rootSeen = false;
  return chartXmlScanner((tag, parents) => {
    if (!parents.length) {
      if (rootSeen || !SHEET_NAMESPACES.has(tag.uri) || !['worksheet', 'chartsheet'].includes(tag.local)) throw new Error('Excel 图表所属工作表命名空间无效。');
      rootSeen = true;
    } else if (parents.length === 1 && tag.local === 'drawing' && SHEET_NAMESPACES.has(tag.uri)) {
      const id = chartRelationId(tag);
      if (ids.includes(id) || ids.length >= 32) throw new Error('Excel 图表来源关系重复或过多。');
      ids.push(id);
    }
  });
}
export function parseDrawingCharts(xml: string): { relationshipId: string; anchorRow?: number }[] {
  const found: { relationshipId: string; anchorRow?: number }[] = [];
  let row: number | undefined, rowText = '', inRow = false;
  const scanner = chartXmlScanner((tag, parents) => {
    if (!parents.length && (tag.local !== 'wsDr' || !DRAWING_NAMESPACES.has(tag.uri))) throw new Error('Excel 图形目录命名空间无效。');
    if (parents.length === 1 && DRAWING_NAMESPACES.has(tag.uri)) row = undefined;
    if (tag.local === 'row' && DRAWING_NAMESPACES.has(tag.uri)
        && parents.length === 3 && parents[2]?.local === 'from' && DRAWING_NAMESPACES.has(parents[2].uri)
        && parents[1] && DRAWING_NAMESPACES.has(parents[1].uri)
        && ['twoCellAnchor', 'oneCellAnchor'].includes(parents[1].local)) { inRow = true; rowText = ''; }
    if (tag.local === 'chart' && CHART_NAMESPACES.has(tag.uri)
        && parents.length === 5 && parents[1] && DRAWING_NAMESPACES.has(parents[1].uri)
        && ['twoCellAnchor', 'oneCellAnchor', 'absoluteAnchor'].includes(parents[1].local)
        && parents[2]?.local === 'graphicFrame' && DRAWING_NAMESPACES.has(parents[2].uri)
        && parents[3]?.local === 'graphic' && TEXT_NAMESPACES.has(parents[3].uri)
        && parents[4]?.local === 'graphicData' && TEXT_NAMESPACES.has(parents[4].uri)
        && CHART_NAMESPACES.has(attr(parents[4], 'uri') || '')) {
      if (found.length >= EXCEL_CHART_LIMITS.charts) throw new Error('当前工作表图表过多，已停止预览。');
      found.push({ relationshipId: chartRelationId(tag), ...(row !== undefined ? { anchorRow: row + 1 } : {}) });
    }
  }, tag => {
    if (inRow && tag.local === 'row' && DRAWING_NAMESPACES.has(tag.uri)) {
      const value = Number(rowText); if (Number.isInteger(value) && value >= 0 && value < 1_048_576) row = value;
      inRow = false;
    }
  }, text => { if (inRow) { rowText += text; if (rowText.length > 16) throw new Error('Excel 图表锚点无效。'); } });
  scanner.write(xml).close(); return found;
}

type Node = { name: string; uri: string; attrs: Record<string, string>; text: string; children: Node[] };
function tree(xml: string): Node {
  const stack: Node[] = []; let root: Node | undefined, nodes = 0;
  const scanner = chartXmlScanner(tag => {
    if (++nodes > EXCEL_CHART_LIMITS.nodes) throw new Error('单张图表结构过大，请在 Excel 中查看。');
    const node: Node = { name: tag.local, uri: tag.uri, attrs: {}, text: '', children: [] };
    for (const [key, value] of Object.entries(tag.attributes)) node.attrs[key] = typeof value === 'string' ? value : value.value;
    if (stack.length) stack.at(-1)!.children.push(node); else root = node;
    stack.push(node);
  }, () => { stack.pop(); }, text => {
    if (!stack.length) return;
    const node = stack.at(-1)!; node.text += text;
    if (node.text.length > 16_384) throw new Error('Excel 图表文字过长。');
  });
  scanner.write(xml).close();
  if (!root || root.name !== 'chartSpace' || !CHART_NAMESPACES.has(root.uri)) throw new Error('Excel 图表命名空间无效。');
  return root;
}
const children = (node: Node | undefined, name?: string) => node?.children.filter(item => CHART_NAMESPACES.has(item.uri) && (!name || item.name === name)) || [];
const child = (node: Node | undefined, name: string) => children(node, name)[0];
const value = (node: Node | undefined, name: string) => child(node, name)?.attrs.val;
function textContent(node: Node | undefined): string {
  if (!node) return '';
  return (TEXT_NAMESPACES.has(node.uri) && node.name === 't' ? node.text : '') + node.children.map(textContent).join('');
}
function numeric(raw: string | undefined): number | undefined {
  if (!raw?.trim()) return undefined;
  const value = Number(raw); return Number.isFinite(value) ? value : undefined;
}
function cache(container: Node | undefined, numericValues: boolean, notices: Set<string>): (string | number | null)[] {
  const ref = child(container, 'numRef') || child(container, 'strRef');
  const data = child(ref, 'numCache') || child(ref, 'strCache') || child(container, 'numLit') || child(container, 'strLit');
  if (!data) { notices.add('部分系列没有已保存的图表缓存；预览不重新计算或读取其他表补值。'); return []; }
  const formula = child(ref, 'f')?.text || '';
  if (formula.includes('[') || formula.includes(']')) notices.add('外部链接仅展示原件已保存的缓存，未访问外部文件。');
  const points = children(data, 'pt');
  const declared = numeric(value(data, 'ptCount'));
  if (declared !== undefined && (!Number.isInteger(declared) || declared < 0 || declared > EXCEL_CHART_LIMITS.points)) throw new Error('单张图表缓存点数超过预览上限。');
  let length = declared || 0; const seen = new Set<number>();
  const indexed: [number, string | number | null][] = [];
  for (const point of points) {
    const index = numeric(point.attrs.idx);
    if (index === undefined || !Number.isInteger(index) || index < 0 || index >= EXCEL_CHART_LIMITS.points || seen.has(index)) throw new Error('Excel 图表缓存索引无效或重复。');
    if (declared !== undefined && index >= declared) throw new Error('Excel 图表缓存索引超出声明点数。');
    seen.add(index); length = Math.max(length, index + 1);
    const raw = child(point, 'v')?.text;
    indexed.push([index, numericValues ? numeric(raw) ?? null : raw ?? null]);
  }
  const result: (string | number | null)[] = Array(length).fill(null);
  indexed.forEach(([index, val]) => { result[index] = val; }); return result;
}

export function parseExcelChart(xml: string, id: string, date1904 = false): ExcelPreviewChart {
  const root = tree(xml), chart = child(root, 'chart'), plot = child(chart, 'plotArea');
  if (!chart || !plot) throw new Error('Excel 图表结构不完整。');
  const notices = new Set<string>(['显示 Excel 原件已保存的图表结果，不重新计算；缓存可能不是最新值。']);
  const titleNode = child(chart, 'title');
  const result: ExcelPreviewChart = { id, title: textContent(titleNode).trim() || '未命名图表', series: [], axes: [], notices: [] };
  const axes = children(plot).filter(node => ['catAx', 'valAx', 'dateAx'].includes(node.name)), unsupportedAxes = new Set<string>();
  for (const axis of axes) {
    const axisId = value(axis, 'axId'); if (!axisId || result.axes.some(item => item.id === axisId)) throw new Error('Excel 图表坐标轴标识无效或重复。');
    const scaling = child(axis, 'scaling'), pos = value(axis, 'axPos');
    if (child(scaling, 'logBase')) { notices.add('对数坐标暂不支持，相关图形未转换为线性坐标。'); unsupportedAxes.add(axisId); }
    result.axes.push({ id: axisId, type: axis.name === 'valAx' ? 'value' : axis.name === 'dateAx' ? 'date' : 'category',
      position: ({ l: 'left', r: 'right', t: 'top', b: 'bottom' } as const)[pos as 'l' | 'r' | 't' | 'b'],
      title: textContent(child(axis, 'title')).trim(), min: numeric(value(scaling, 'min')), max: numeric(value(scaling, 'max')),
      numFmt: child(axis, 'numFmt')?.attrs.formatCode, crossAxisId: value(axis, 'crossAx'), inverse: value(scaling, 'orientation') === 'maxMin' });
  }
  let totalPoints = 0;
  for (const [groupIndex, group] of children(plot).entries()) {
    const series = children(group, 'ser'); if (!series.length) continue;
    const kind = ({ lineChart: 'line', barChart: 'bar', areaChart: 'area' } as const)[group.name as 'lineChart' | 'barChart' | 'areaChart'];
    if (!kind) { notices.add(`暂不支持 ${group.name}，未将其转换为其他图形。`); continue; }
    const axisIds = children(group, 'axId').map(node => node.attrs.val);
    if (axisIds.some(axis => !result.axes.some(item => item.id === axis) || unsupportedAxes.has(axis!))) {
      notices.add('部分图形坐标轴不存在或暂不支持，已跳过；未猜测坐标轴绑定。'); continue;
    }
    const categoriesAxes = result.axes.filter(axis => axisIds.includes(axis.id) && axis.type !== 'value');
    const valueAxes = result.axes.filter(axis => axisIds.includes(axis.id) && axis.type === 'value');
    if (axisIds.length !== 2 || new Set(axisIds).size !== 2 || categoriesAxes.length !== 1 || valueAxes.length !== 1
        || valueAxes[0]!.crossAxisId !== categoriesAxes[0]!.id
        || !result.axes.some(axis => axis.id === categoriesAxes[0]!.crossAxisId && axis.type === 'value')) {
      notices.add('部分图形没有唯一且有效的分类/数值轴关联，已跳过；未使用默认主轴代替。'); continue;
    }
    const categoryAxis = categoriesAxes[0]!, numberAxis = valueAxes[0]!;
    const grouping = value(group, 'grouping') || 'standard';
    if (!['standard', 'clustered', 'stacked', 'percentStacked'].includes(grouping)) { notices.add('部分图形分组模式不支持，已跳过。'); continue; }
    for (const serie of series) {
      if (result.series.length >= EXCEL_CHART_LIMITS.series) throw new Error('单张图表系列数量超过预览上限。');
      const values = cache(child(serie, 'val'), true, notices) as (number | null)[];
      let categories = cache(child(serie, 'cat'), false, notices);
      let categoryTimes: (number | null)[] | undefined;
      if (!categories.length && values.length) {
        if (categoryAxis.type === 'date') {
          notices.add('部分日期轴没有已保存的分类缓存，不以点序号补造日期。');
          categories = Array(values.length).fill(null);
        } else {
          notices.add('部分系列没有已保存的分类标签，仅以原缓存点序号展示。');
          categories = values.map((_, index) => String(index + 1));
        }
      }
      if (categoryAxis?.type === 'date') {
        const fmt = categoryAxis.numFmt || child(child(child(serie, 'cat'), 'numRef'), 'numCache')?.children.find(node => node.name === 'formatCode')?.text;
        const numericCategories = !!(child(child(serie, 'cat'), 'numRef') || child(child(serie, 'cat'), 'numLit'));
        categoryTimes = categories.map(label => {
          const serial = typeof label === 'number' ? label : numeric(label || undefined);
          if (!numericCategories || serial === undefined) return null;
          const parsed = XLSX.SSF.parse_date_code(serial, { date1904 }) as { y: number; m: number; d: number; H: number; M: number; S: number; u: number } | null;
          if (!parsed || parsed.y < 1 || parsed.y > 9999 || parsed.d < 1) return null;
          const timestamp = new Date(0); timestamp.setUTCFullYear(parsed.y, parsed.m - 1, parsed.d);
          timestamp.setUTCHours(parsed.H, parsed.M, parsed.S, Math.round((parsed.u || 0) * 1000));
          return timestamp.getUTCFullYear() === parsed.y && timestamp.getUTCMonth() === parsed.m - 1
            && timestamp.getUTCDate() === parsed.d ? timestamp.getTime() : null;
        });
        if (categoryTimes.some(time => time === null)) notices.add('部分日期轴缓存缺失或无法还原真实日期间距，未猜测日期。');
        categories = categories.map(label => {
          const serial = typeof label === 'number' ? label : numeric(label || undefined);
          return serial === undefined || !numericCategories ? label : XLSX.SSF.format(fmt && XLSX.SSF.is_date(fmt) ? fmt : 'yyyy-mm-dd', serial, { date1904 });
        });
      }
      totalPoints += Math.max(values.length, categories.length);
      if (totalPoints > EXCEL_CHART_LIMITS.points) throw new Error('单张图表总缓存点数超过 100,000，已停止预览。');
      const tx = child(serie, 'tx'), label = child(tx, 'v')?.text || child(child(child(tx, 'strRef'), 'strCache'), 'pt')?.children.find(node => node.name === 'v')?.text;
      const colorNode = child(serie, 'spPr');
      const findColor = (node: Node | undefined): string | undefined => {
        if (!node) return undefined;
        if (node.name === 'srgbClr' && TEXT_NAMESPACES.has(node.uri) && /^[0-9a-f]{6}$/i.test(node.attrs.val || '')) return '#' + node.attrs.val;
        return node.children.map(findColor).find(Boolean);
      };
      result.series.push({ name: label?.slice(0, 512) || `系列 ${result.series.length + 1}`, type: kind, values,
        categories, color: findColor(colorNode), axisId: numberAxis?.id, categoryAxisId: categoryAxis?.id,
        grouping: grouping === 'clustered' ? 'standard' : grouping as 'standard' | 'stacked' | 'percentStacked',
        horizontal: group.name === 'barChart' && value(group, 'barDir') === 'bar', groupId: `group-${groupIndex}`,
        categoryTimes,
        smooth: value(serie, 'smooth') === '1' });
    }
  }
  if (!result.series.length) notices.add('此图没有可预览的已支持系列。');
  result.notices = [...notices]; return result;
}
