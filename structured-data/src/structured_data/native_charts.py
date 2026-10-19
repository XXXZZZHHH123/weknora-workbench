"""Native OOXML charts linked to approved observations, never OCR or cached values.

The chart index is disposable derived metadata. Dataset snapshots remain immutable.
Only internal one-column value references with unambiguous source series are enabled.
Source observation dates remain authoritative even for seasonal overlay axes.
"""
from collections import Counter
from hashlib import sha256
import json
import logging
import os
from pathlib import Path
import posixpath
import re
import tempfile
import time
from xml.etree import ElementTree as ET
from zipfile import ZipFile

VERSION = 'native-charts-v6-verified-source-dates'
logger = logging.getLogger(__name__)
NS = {'s': 'http://schemas.openxmlformats.org/spreadsheetml/2006/main',
      'r': 'http://schemas.openxmlformats.org/officeDocument/2006/relationships',
      'c': 'http://schemas.openxmlformats.org/drawingml/2006/chart',
      'a': 'http://schemas.openxmlformats.org/drawingml/2006/main',
      'xdr': 'http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing'}


def reference(formula):
    """Reject external files, names, unions, formulas and rectangular/multi-column data."""
    if not isinstance(formula, str) or '[' in formula or ']' in formula:
        return None
    match = re.fullmatch(r"(?:'((?:[^']|'')+)'|([^'!]+))!\$?([A-Z]{1,3})\$?(\d+)(?::\$?([A-Z]{1,3})\$?(\d+))?", formula)
    if not match:
        return None
    quoted, plain, col, start, endcol, end = match.groups()
    start, end = int(start), int(end or start)
    if (endcol and endcol != col) or not 1 <= start <= end <= 1048576:
        return None
    return {'sheet': (quoted.replace("''", "'") if quoted else plain),
            'column': col, 'start_row': start, 'end_row': end}


def _relations(z, part):
    path = _relationship_part(part)
    if path not in z.namelist():
        return {}
    return _parse_relationships(z.read(path), part)


def _relationship_part(part):
    return posixpath.join(posixpath.dirname(part), '_rels', posixpath.basename(part) + '.rels')


def _parse_relationships(data, part):
    return {r.attrib['Id']: {'type': r.attrib.get('Type'),
            'external': r.attrib.get('TargetMode') == 'External',
            'target': posixpath.normpath(posixpath.join(posixpath.dirname(part), r.attrib['Target']))
            if not r.attrib['Target'].startswith('/') else r.attrib['Target'].lstrip('/')}
            for r in ET.fromstring(data)}


_RELATIONSHIP_BASE = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships/'


SUPPORTED_CHART_GROUPS = {
    'areaChart', 'area3DChart', 'barChart', 'bar3DChart', 'bubbleChart',
    'doughnutChart', 'lineChart', 'line3DChart', 'ofPieChart', 'pieChart',
    'pie3DChart', 'radarChart', 'scatterChart', 'stockChart', 'surfaceChart',
    'surface3DChart',
}


class ChartReadBudgetExceeded(RuntimeError):
    """The selected real chart parts exceed the bounded source-read budget."""


def is_safe_chart_part(part):
    if not isinstance(part, str):
        return False
    try:
        if len(part.encode('utf-8')) > 512:
            return False
    except UnicodeEncodeError:
        return False
    return ('\\' not in part and part.startswith('xl/charts/')
            and part.endswith('.xml') and posixpath.normpath(part) == part
            and all(piece not in ('', '.', '..') for piece in part.split('/')))


def _real_chart_bindings(z):
    """Resolve chart parts and host sheets through live OOXML relationships."""
    names = set(z.namelist())
    workbook = 'xl/workbook.xml'
    if workbook not in names:
        return []
    bindings = []
    links = _relations(z, workbook)
    for sheet in ET.fromstring(z.read(workbook)).findall('s:sheets/s:sheet', NS):
        sheet_name = sheet.get('name')
        link = links.get(sheet.get('{%s}id' % NS['r']), {})
        if link.get('external') or link.get('target') not in names:
            continue
        sheet_part = link['target']
        sheet_links = _relations(z, sheet_part)
        for drawing in ET.fromstring(z.read(sheet_part)).findall('s:drawing', NS):
            dlink = sheet_links.get(drawing.get('{%s}id' % NS['r']), {})
            if dlink.get('external') or dlink.get('target') not in names:
                continue
            drawing_part = dlink['target']
            drawing_links = _relations(z, drawing_part)
            for anchor in ET.fromstring(z.read(drawing_part)):
                row = int(anchor.findtext('xdr:from/xdr:row', '0', NS)) + 1
                for obj in anchor.findall('.//c:chart', NS):
                    clink = drawing_links.get(obj.get('{%s}id' % NS['r']), {})
                    target = clink.get('target')
                    if (clink.get('external') or target not in names
                            or not is_safe_chart_part(target)):
                        continue
                    bindings.append({'sheet': sheet_name, 'part': target, 'anchor_row': row})
    return bindings


def _parse_chart_part(z, binding):
    cp = binding['part']
    root = ET.fromstring(z.read(cp))
    cid = sha256((binding['sheet'] + ':' + cp).encode()).hexdigest()[:20]
    title = ''.join(t.text or '' for t in root.findall('c:chart/c:title//a:t', NS))
    if not title:
        title = root.findtext('c:chart/c:title//c:strCache/c:pt/c:v', '', NS)
    axis_nodes = [axis for axis in root.findall('c:chart/c:plotArea/*', NS)
                  if axis.tag.rsplit('}', 1)[-1] in ('catAx', 'valAx', 'dateAx', 'serAx')]
    axes = []
    for axis in axis_nodes:
        axis_title = ''.join(t.text or '' for t in axis.findall('c:title//a:t', NS))
        id_node = axis.find('c:axId', NS)
        cross_node = axis.find('c:crossAx', NS)
        display_units = axis.find('c:units/c:builtInUnit', NS)
        custom_units = axis.find('c:units/c:custUnit', NS)
        if display_units is None:
            display_units = axis.find('c:dispUnits/c:builtInUnit', NS)
        if custom_units is None:
            custom_units = axis.find('c:dispUnits/c:custUnit', NS)
        axes.append({
            'id': id_node.get('val') if id_node is not None else None,
            'type': axis.tag.rsplit('}', 1)[-1],
            'title': axis_title,
            'cross_ax': cross_node.get('val') if cross_node is not None else None,
            'display_units': ({'built_in': display_units.get('val') if display_units is not None else None,
                               'custom': custom_units.get('val') if custom_units is not None else None}
                              if display_units is not None or custom_units is not None else None),
        })
    curves = []
    groups = root.findall('c:chart/c:plotArea/*', NS)
    unsupported_groups = [group.tag.rsplit('}', 1)[-1] for group in groups
                         if group.findall('c:ser', NS)
                         and group.tag.rsplit('}', 1)[-1] not in SUPPORTED_CHART_GROUPS]
    for group in groups:
        for ser in group.findall('c:ser', NS):
            label = ser.findtext('c:tx/c:v', None, NS) or ser.findtext('c:tx//c:strCache/c:pt/c:v', '', NS)
            values = ser.findtext('c:val/c:numRef/c:f', None, NS) or ser.findtext('c:yVal/c:numRef/c:f', None, NS)
            cats = ser.findtext('c:cat/c:numRef/c:f', None, NS) or ser.findtext('c:cat/c:strRef/c:f', None, NS) or ser.findtext('c:xVal/c:numRef/c:f', None, NS)
            axis_ids = [node.get('val') for node in group.findall('c:axId', NS)
                        if node.get('val') is not None]
            curves.append({'label': label or f'曲线 {len(curves)+1}', 'value_ref': values,
                           'category_ref': cats, 'reference': reference(values),
                           'axis_ids': axis_ids,
                           'chart_type': group.tag.rsplit('}', 1)[-1], 'status': 'pending_source',
                           'reason': '', 'series_id': None})
    return ({'chart_id': cid, 'sheet': binding['sheet'], 'title': title or '未命名图表',
             'part': cp, 'anchor_row': binding['anchor_row'], 'axes': axes,
             'axis_titles': [axis['title'] for axis in axes if axis['title']],
             'curves': curves}, unsupported_groups)


class _TargetedXMLBudget:
    def __init__(self, archive, *, max_parts=1024, max_part_bytes=1024 * 1024,
                 max_total_bytes=8 * 1024 * 1024):
        self.archive = archive
        self.infos = {info.filename: info for info in archive.infolist()}
        self.max_parts = max_parts
        self.max_part_bytes = max_part_bytes
        self.max_total_bytes = max_total_bytes
        self.total_bytes = 0
        self.cache = {}

    def read(self, part):
        if part in self.cache:
            return self.cache[part]
        info = self.infos.get(part)
        if info is None:
            return None
        if info.file_size > self.max_part_bytes:
            raise ChartReadBudgetExceeded('relationship XML exceeds per-part source evidence budget')
        if len(self.cache) >= self.max_parts or self.total_bytes + info.file_size > self.max_total_bytes:
            raise ChartReadBudgetExceeded('relationship XML exceeds total source evidence budget')
        data = self.archive.read(part)
        if len(data) > self.max_part_bytes or self.total_bytes + len(data) > self.max_total_bytes:
            raise ChartReadBudgetExceeded('relationship XML exceeds source evidence budget')
        self.cache[part] = data
        self.total_bytes += len(data)
        return data


def _targeted_chart_bindings(z, requested_parts):
    """Resolve candidate ownership without reading unrelated worksheet XML."""
    budget = _TargetedXMLBudget(z)
    names = set(budget.infos)
    workbook_part = 'xl/workbook.xml'
    workbook_data = budget.read(workbook_part)
    if workbook_data is None:
        return []
    workbook_rels = budget.read(_relationship_part(workbook_part))
    if workbook_rels is None:
        return []
    links = _parse_relationships(workbook_rels, workbook_part)
    workbook_root = ET.fromstring(workbook_data)
    bindings = []

    for sheet in workbook_root.findall('s:sheets/s:sheet', NS):
        sheet_name = sheet.get('name')
        sheet_link = links.get(sheet.get('{%s}id' % NS['r']), {})
        if (sheet_link.get('type') != _RELATIONSHIP_BASE + 'worksheet'
                or sheet_link.get('external') or sheet_link.get('target') not in names):
            continue
        sheet_part = sheet_link['target']
        sheet_rels_data = budget.read(_relationship_part(sheet_part))
        if sheet_rels_data is None:
            continue
        sheet_links = _parse_relationships(sheet_rels_data, sheet_part)

        for sheet_rel_id, drawing_link in sheet_links.items():
            drawing_part = drawing_link.get('target')
            if (drawing_link.get('type') != _RELATIONSHIP_BASE + 'drawing'
                    or drawing_link.get('external') or drawing_part not in names
                    or not drawing_part.startswith('xl/drawings/')):
                continue
            drawing_rels_data = budget.read(_relationship_part(drawing_part))
            if drawing_rels_data is None:
                continue
            drawing_links = _parse_relationships(drawing_rels_data, drawing_part)
            chart_rel_ids = {rid for rid, link in drawing_links.items()
                             if link.get('type') == _RELATIONSHIP_BASE + 'chart'
                             and not link.get('external') and link.get('target') in requested_parts}
            if not chart_rel_ids:
                continue

            drawing_data = budget.read(drawing_part)
            if drawing_data is None:
                continue
            drawing_root = ET.fromstring(drawing_data)
            selected = []
            for anchor in drawing_root:
                row = int(anchor.findtext('xdr:from/xdr:row', '0', NS)) + 1
                for obj in anchor.findall('.//c:chart', NS):
                    rid = obj.get('{%s}id' % NS['r'])
                    if rid not in chart_rel_ids:
                        continue
                    target = drawing_links[rid].get('target')
                    if is_safe_chart_part(target):
                        selected.append({'sheet': sheet_name, 'part': target,
                                         'anchor_row': row})
            if not selected:
                continue

            # The .rels entry alone is not enough: a worksheet must actually
            # reference this drawing relationship. Read only this owner sheet.
            sheet_data = budget.read(sheet_part)
            if sheet_data is None:
                continue
            sheet_root = ET.fromstring(sheet_data)
            drawing_ids = {node.get('{%s}id' % NS['r'])
                           for node in sheet_root.findall('s:drawing', NS)}
            if sheet_rel_id not in drawing_ids or sheet_links[sheet_rel_id].get('target') != drawing_part:
                continue
            bindings.extend(selected)
    return bindings


def extract_parts(path, requested_parts, *, max_parts=32,
                  max_part_bytes=2 * 1024 * 1024, max_total_bytes=8 * 1024 * 1024):
    """Parse only selected chart parts after resolving them in live relationships."""
    requested = set(requested_parts)
    if any(not is_safe_chart_part(part) for part in requested):
        return [], ['invalid_candidate_part']
    if len(requested) > max_parts:
        raise ChartReadBudgetExceeded('selected chart part count exceeds source evidence budget')
    charts = []
    reasons = []
    with ZipFile(path) as z:
        infos = {info.filename: info for info in z.infolist()}
        for part in requested:
            info = infos.get(part)
            if info is None:
                reasons.append('candidate_part_not_in_source')
                continue
            if info.file_size > max_part_bytes:
                raise ChartReadBudgetExceeded('selected chart part exceeds per-part source evidence budget')
        bindings = _targeted_chart_bindings(z, requested)
        actual = [binding for binding in bindings if binding['part'] in requested]
        actual_parts = {binding['part'] for binding in actual}
        if requested - actual_parts:
            reasons.append('candidate_part_not_in_live_relationships')
        total_size = sum(infos[binding['part']].file_size for binding in actual
                         if binding['part'] in infos)
        if total_size > max_total_bytes:
            raise ChartReadBudgetExceeded('selected chart parts exceed total source evidence budget')
        for binding in actual:
            try:
                chart, unsupported = _parse_chart_part(z, binding)
            except (ET.ParseError, KeyError, ValueError):
                reasons.append('selected_chart_part_parse_failed')
                continue
            if unsupported:
                reasons.append('selected_chart_type_unsupported')
                continue
            charts.append(chart)
    return charts, list(dict.fromkeys(reasons))


def extract(path):
    """Read all real drawing objects, rather than unused relationships."""
    charts = []
    with ZipFile(path) as z:
        for binding in _real_chart_bindings(z):
            chart, _ = _parse_chart_part(z, binding)
            charts.append(chart)
    return charts


def _range_sql(ref):
    return "regexp_extract(source_cell, '^([A-Z]+)', 1) = ? AND try_cast(regexp_extract(source_cell, '([0-9]+)$', 1) AS INTEGER) BETWEEN ? AND ?", [ref['column'], ref['start_row'], ref['end_row']]


def build(store, dataset_id):
    folder = store._path(dataset_id)
    series = json.loads((folder / 'series.json').read_text())
    byid = {s['series_id']: s for s in series}
    charts = extract(folder / 'source.xlsx') if (folder / 'source.xlsx').is_file() else []
    views = []; curve_views = []
    with store._connect(dataset_id) as con:
        # Batch the reference join: do not rescan a million-row workbook per curve.
        refs = {tuple(c['reference'].values()): c['reference'] for ch in charts for c in ch['curves'] if c['reference']}
        keys = list(refs)
        con.execute('CREATE TEMP TABLE chart_sources(series_id VARCHAR, sheet VARCHAR)')
        if series:
            con.executemany('INSERT INTO chart_sources VALUES (?,?)', [(s['series_id'], s['sheet']) for s in series])
        con.execute('CREATE TEMP TABLE chart_refs(ref_id INTEGER, sheet VARCHAR, col VARCHAR, start_row INTEGER, end_row INTEGER)')
        if keys:
            con.executemany('INSERT INTO chart_refs VALUES (?,?,?,?,?)', [(i, *key) for i,key in enumerate(keys)])
        stats = con.execute("""WITH cells AS (
            SELECT o.series_id,s.sheet,o.date,o.value,
                regexp_extract(o.source_cell,'^([A-Z]+)',1) AS col,
                try_cast(regexp_extract(o.source_cell,'([0-9]+)$',1) AS INTEGER) AS rownum
            FROM observations o JOIN chart_sources s ON o.series_id=s.series_id)
            SELECT r.ref_id,c.series_id,count(*),count(c.value),min(c.date),max(c.date),
                min(c.date) FILTER(WHERE c.value IS NOT NULL),max(c.date) FILTER(WHERE c.value IS NOT NULL)
            FROM chart_refs r JOIN cells c ON c.sheet=r.sheet AND c.col=r.col
                AND c.rownum BETWEEN r.start_row AND r.end_row GROUP BY r.ref_id,c.series_id""").fetchall()
        cache = {key: [] for key in keys}
        for row in stats:
            cache[keys[row[0]]].append(row[1:])
        for chart in charts:
            groups = {}
            for curve in chart['curves']:
                ref = curve['reference']
                if not ref:
                    curve.update(status='unsupported', reason='没有可核对的工作簿内单列引用；外部链接、命名范围、数组和多列引用暂不支持。')
                    continue
                ids = [s['series_id'] for s in series if s['sheet'] == ref['sheet']]
                if not ids:
                    curve['reason'] = '源工作表尚未导入，请先确认源表映射。'
                    continue
                key = tuple(ref.values())
                matches = cache[key]
                if len(matches) != 1:
                    curve.update(status='needs_review' if matches else 'pending_source', reason='引用范围对应多个指标，需核对源表映射。' if matches else '引用范围没有已导入记录，请先确认源表及区块范围。')
                    continue
                sid, count, valid, first, last, vfirst, vlast = matches[0]
                source = byid[sid]
                curve.update(source_series_id=sid, source_metric=source['metric'], unit=source.get('unit'),
                             record_count=count, valid_count=valid,
                             first_date=str(first) if first else None, last_date=str(last) if last else None)
                curve.update(first_valid_date=str(vfirst) if vfirst else None, last_valid_date=str(vlast) if vlast else None)
                if not valid:
                    curve.update(status='no_data', reason='已关联的源单元格没有有效数值；不使用图表缓存补值。')
                    continue
                curve.update(status='linked', reason='已关联源单元格；日期使用源表已核验日期，图表缓存不参与取数。')
                cat = reference(curve['category_ref'])
                curve['date_notice'] = '图例是原始标签，不自动当作年份或合约含义；曲线日期仅来自已核验源单元格，图表横轴与缓存不补造日期。'
                groups.setdefault(sid, []).append(curve)
            for sid, curves in groups.items():
                source = byid[sid]
                view_id = 'chart_' + sha256((chart['chart_id'] + ':' + sid).encode()).hexdigest()[:24]
                # The catalogue groups a chart; each UI curve receives its own bounded view.
                view = dict(source, series_id=view_id, metric=chart['title'], source_metric=source['metric'],
                            chart_sheet=chart['sheet'], chart_id=chart['chart_id'], source_series_id=sid,
                            chart_ranges=[c['reference'] for c in curves], chart_legends=[c['label'] for c in curves],
                            chart_curve_sources=[{key: c.get(key) for key in
                                ('label', 'reference', 'source_series_id', 'first_date', 'last_date',
                                 'first_valid_date', 'last_valid_date', 'date_notice')} for c in curves],
                            description=f"图表 {chart['sheet']} / {chart['title']} → {source['sheet']} / {source['metric']}；仅查询已关联曲线的源单元格范围。")
                view['chart_excluded_curves'] = [{'label':c['label'],'reason':c['reason']} for c in chart['curves'] if c['status']!='linked']
                view['first_valid_date'] = min((c['first_valid_date'] for c in curves if c['first_valid_date']), default=None)
                view['last_valid_date'] = max((c['last_valid_date'] for c in curves if c['last_valid_date']), default=None)
                views.append(view)
                for i, curve in enumerate(curves):
                    curve_id='chart_'+sha256((view_id+':'+str(i)+':'+str(curve['reference'])).encode()).hexdigest()[:24]
                    curve['series_id']=curve_id
                    curve_views.append(dict(view, series_id=curve_id,
                        metric=chart['title']+' / '+curve['label'], chart_title=chart['title'],
                        chart_ranges=[curve['reference']], chart_legends=[curve['label']],
                        chart_curve_sources=[{key: curve.get(key) for key in
                            ('label', 'reference', 'source_series_id', 'first_date', 'last_date',
                             'first_valid_date', 'last_valid_date', 'date_notice')}],
                        first_valid_date=curve['first_valid_date'],last_valid_date=curve['last_valid_date'],
                        description=view['description']+' 图例：'+curve['label']))
            counts = Counter(c['status'] for c in chart['curves'])
            chart['status'] = 'linked' if counts.get('linked') == len(chart['curves']) and chart['curves'] else 'partial' if counts.get('linked') else 'pending_source'
            chart['status_counts'] = dict(counts)
    return {'version': VERSION, 'dataset_id': dataset_id, 'chart_count': len(charts),
            'charts': charts, 'views': views, 'curve_views':curve_views, 'notice': '只读原生图表引用；数值取自已导入的源单元格，公式使用Excel保存结果。图片不解析。未导入区域和图表缓存不会补入数据。'}


def index(store, dataset_id):
    started = time.perf_counter()
    folder = store._path(dataset_id)
    path = folder / (VERSION + '.json')
    if path.is_file():
        try:
            result = json.loads(path.read_text())
        except Exception as exc:
            logger.warning('native_chart_index cache_read_failed dataset_id=%s elapsed_ms=%d error_type=%s',
                           dataset_id, round((time.perf_counter()-started)*1000), type(exc).__name__)
            raise
        logger.info('native_chart_index cache_hit dataset_id=%s elapsed_ms=%d charts=%d',
                    dataset_id, round((time.perf_counter()-started)*1000),
                    result.get('chart_count', 0) if isinstance(result, dict) else 0)
        return result
    logger.info('native_chart_index cache_miss dataset_id=%s elapsed_ms=%d',
                dataset_id, round((time.perf_counter()-started)*1000))
    try:
        result = build(store, dataset_id)
        # A derived sidecar does not change any published series, observations or mappings.
        fd, tmp = tempfile.mkstemp(prefix='.charts-', dir=folder)
        try:
            with os.fdopen(fd, 'w') as f:
                json.dump(result, f, ensure_ascii=False)
            os.replace(tmp, path)
        finally:
            if Path(tmp).exists(): Path(tmp).unlink()
    except Exception as exc:
        logger.warning('native_chart_index build_failed dataset_id=%s elapsed_ms=%d error_type=%s',
                       dataset_id, round((time.perf_counter()-started)*1000), type(exc).__name__)
        raise
    logger.info('native_chart_index build_complete dataset_id=%s elapsed_ms=%d charts=%d',
                dataset_id, round((time.perf_counter()-started)*1000), result.get('chart_count', 0))
    return result


def catalog(store, dataset_id, sheet=None, question=''):
    """Question text never restricts authorized sources or assigns legend meaning."""
    base = json.loads((store._path(dataset_id) / 'series.json').read_text())
    idx = index(store, dataset_id)
    views = [*idx['views'], *idx.get('curve_views', [])]
    aliases = {}
    for view in views:
        aliases.setdefault(view['source_series_id'], set()).add(view.get('chart_title', view['metric']))
    for source in base:
        titles = sorted(title for title in aliases.get(source['series_id'], set()) if title != source['metric'])
        if titles:
            source['chart_titles'] = titles
    all_sources = [*base, *views]
    if sheet is not None:
        return [source for source in all_sources if source['sheet'] == sheet or source.get('chart_sheet') == sheet]
    return all_sources
