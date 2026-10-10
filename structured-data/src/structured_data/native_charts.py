"""Native OOXML charts linked to approved observations, never OCR or cached values.

The chart index is disposable derived metadata. Dataset snapshots remain immutable.
Only internal one-column value references with unambiguous source series are enabled.
Source observation dates remain authoritative even for seasonal overlay axes.
"""
from collections import Counter
from hashlib import sha256
import json
import os
from pathlib import Path
import posixpath
import re
import tempfile
from xml.etree import ElementTree as ET
from zipfile import ZipFile

VERSION = 'native-charts-v4'
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
    path = posixpath.join(posixpath.dirname(part), '_rels', posixpath.basename(part) + '.rels')
    if path not in z.namelist():
        return {}
    return {r.attrib['Id']: {'external': r.attrib.get('TargetMode') == 'External',
            'target': posixpath.normpath(posixpath.join(posixpath.dirname(part), r.attrib['Target']))
            if not r.attrib['Target'].startswith('/') else r.attrib['Target'].lstrip('/')}
            for r in ET.fromstring(z.read(path))}


def extract(path):
    """Read only real drawing objects, rather than unused drawing relationships."""
    charts = []
    with ZipFile(path) as z:
        links = _relations(z, 'xl/workbook.xml')
        for sheet in ET.fromstring(z.read('xl/workbook.xml')).findall('s:sheets/s:sheet', NS):
            link = links.get(sheet.get('{%s}id' % NS['r']), {})
            if link.get('external') or link.get('target') not in z.namelist():
                continue
            part = link['target']
            slinks = _relations(z, part)
            for drawing in ET.fromstring(z.read(part)).findall('s:drawing', NS):
                dlink = slinks.get(drawing.get('{%s}id' % NS['r']), {})
                if dlink.get('external') or dlink.get('target') not in z.namelist():
                    continue
                dp = dlink['target']; dlinks = _relations(z, dp)
                for anchor in ET.fromstring(z.read(dp)):
                    for obj in anchor.findall('.//c:chart', NS):
                        clink = dlinks.get(obj.get('{%s}id' % NS['r']), {})
                        if clink.get('external') or clink.get('target') not in z.namelist():
                            continue
                        cp = clink['target']; root = ET.fromstring(z.read(cp))
                        cid = sha256((sheet.get('name') + ':' + cp).encode()).hexdigest()[:20]
                        title = ''.join(t.text or '' for t in root.findall('c:chart/c:title//a:t', NS))
                        if not title:
                            title = root.findtext('c:chart/c:title//c:strCache/c:pt/c:v', '', NS)
                        axes = [''.join(t.text or '' for t in axis.findall('c:title//a:t', NS))
                                for axis in root.findall('c:chart/c:plotArea/*', NS) if axis.tag.endswith('Ax')]
                        curves = []
                        for group in root.findall('c:chart/c:plotArea/*', NS):
                            for i, ser in enumerate(group.findall('c:ser', NS)):
                                label = ser.findtext('c:tx/c:v', None, NS) or ser.findtext('c:tx//c:strCache/c:pt/c:v', '', NS)
                                values = ser.findtext('c:val/c:numRef/c:f', None, NS) or ser.findtext('c:yVal/c:numRef/c:f', None, NS)
                                cats = ser.findtext('c:cat/c:numRef/c:f', None, NS) or ser.findtext('c:cat/c:strRef/c:f', None, NS) or ser.findtext('c:xVal/c:numRef/c:f', None, NS)
                                curves.append({'label': label or f'曲线 {len(curves)+1}', 'value_ref': values,
                                               'category_ref': cats, 'reference': reference(values),
                                               'chart_type': group.tag.rsplit('}', 1)[-1], 'status': 'pending_source',
                                               'reason': '', 'series_id': None})
                        charts.append({'chart_id': cid, 'sheet': sheet.get('name'), 'title': title or '未命名图表',
                                       'part': cp, 'anchor_row': int(anchor.findtext('xdr:from/xdr:row', '0', NS)) + 1,
                                       'axis_titles': [a for a in axes if a], 'curves': curves})
    return charts


def _range_sql(ref):
    return "regexp_extract(source_cell, '^([A-Z]+)', 1) = ? AND try_cast(regexp_extract(source_cell, '([0-9]+)$', 1) AS INTEGER) BETWEEN ? AND ?", [ref['column'], ref['start_row'], ref['end_row']]


def calendar_year(curve, curves):
    """A four-digit legend is not intrinsically a year (e.g. contract 1901)."""
    label=curve['label'].strip()
    explicit=re.fullmatch(r'(19\d{2}|20\d{2})年', label)
    if explicit:return int(explicit[1])
    numeric=[int(c['label'].strip()) for c in curves if re.fullmatch(r'\d{4}',c['label'].strip())]
    # Consecutive calendar cohorts provide evidence; mixed centuries or YYMM
    # sequences don't. Unknown labels retain their literal meaning.
    years=sorted(set(numeric))
    if (label.isdigit() and int(label) in years and len(years)>=2
        and all(1900<=n<=2099 for n in years)
        and all(b-a==1 for a,b in zip(years,years[1:]))):
        return int(label)
    return None


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
                year = calendar_year(curve, chart['curves'])
                if year and (not first or not last or first.year != year or last.year != year):
                    curve.update(status='needs_review', reason='图例年份与已导入的源数据日期不一致，或缺少核验日期；不套用横轴年份。')
                    continue
                if not valid:
                    curve.update(status='no_data', reason='已关联的源单元格没有有效数值；不使用图表缓存补值。')
                    continue
                curve.update(status='linked', reason='已关联源单元格；日期使用源表已核验日期，图表缓存不参与取数。')
                cat = reference(curve['category_ref'])
                if year or not cat:
                    curve['date_notice'] = '曲线按源表日期查询；图上的月份横轴或缺省分类不作为完整日期。'
                groups.setdefault(sid, []).append(curve)
            for sid, curves in groups.items():
                source = byid[sid]
                view_id = 'chart_' + sha256((chart['chart_id'] + ':' + sid).encode()).hexdigest()[:24]
                # The catalogue groups a chart; each UI curve receives its own bounded view.
                view = dict(source, series_id=view_id, metric=chart['title'], source_metric=source['metric'],
                            chart_sheet=chart['sheet'], chart_id=chart['chart_id'], source_series_id=sid,
                            chart_ranges=[c['reference'] for c in curves], chart_legends=[c['label'] for c in curves],
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
                        first_valid_date=curve['first_valid_date'],last_valid_date=curve['last_valid_date'],
                        description=view['description']+' 图例：'+curve['label']))
            counts = Counter(c['status'] for c in chart['curves'])
            chart['status'] = 'linked' if counts.get('linked') == len(chart['curves']) and chart['curves'] else 'partial' if counts.get('linked') else 'pending_source'
            chart['status_counts'] = dict(counts)
    return {'version': VERSION, 'dataset_id': dataset_id, 'chart_count': len(charts),
            'charts': charts, 'views': views, 'curve_views':curve_views, 'notice': '只读原生图表引用；数值取自已导入的源单元格，公式使用Excel保存结果。图片不解析。未导入区域和图表缓存不会补入数据。'}


def index(store, dataset_id):
    folder = store._path(dataset_id)
    path = folder / (VERSION + '.json')
    if path.is_file():
        return json.loads(path.read_text())
    result = build(store, dataset_id)
    # A derived sidecar does not change any published series, observations or mappings.
    fd, tmp = tempfile.mkstemp(prefix='.charts-', dir=folder)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(result, f, ensure_ascii=False)
        os.replace(tmp, path)
    finally:
        if Path(tmp).exists(): Path(tmp).unlink()
    return result


def catalog(store, dataset_id, sheet=None, question=''):
    base = json.loads((store._path(dataset_id) / 'series.json').read_text())
    idx = index(store, dataset_id)
    views = idx['views']
    legend_question=re.sub(r'\d{4}[-/年]\d{1,2}[-/月]\d{1,2}日?', '',question)
    literal_curves=[v for v in idx.get('curve_views',[]) if any(re.search(r'(?<![0-9A-Za-z_])'+re.escape(label)+r'(?![0-9A-Za-z_])', legend_question) for label in v['chart_legends'])]
    if literal_curves:
        curve_charts={v['chart_id'] for v in literal_curves}
        views=[v for v in views if v['chart_id'] not in curve_charts]+literal_curves
    chart_sheets = {c['sheet'] for c in idx['charts']}
    mentioned = {name for name in chart_sheets if name in question}
    titles = {c['title'] for c in idx['charts'] if len(c['title'])>3 and c['title'] in question}
    # A year matching a seasonal legend is not a request to restrict sources.
    # Cross-table analysis must retain its explicitly named raw fields.
    chart_request = any(word in question for word in ('图表','图中','曲线','图里'))
    # A sheet may contain both tables and charts. Merely mentioning its name
    # (including an agent's follow-up context) must not hide its real columns.
    # A chart-only sheet still needs its source-backed views when explicitly scoped.
    chart_only_scope = sheet in chart_sheets and not any(s['sheet'] == sheet for s in base)
    if chart_only_scope or (chart_request and (sheet in chart_sheets or (mentioned and sheet is None))):
        scopes = {sheet} if sheet else mentioned
        selected = [v for v in views if v['chart_sheet'] in scopes and (not titles or v.get('chart_title',v['metric']) in titles)]
        return selected
    if sheet:
        return [s for s in base if s['sheet'] == sheet]
    # Keep the general catalogue compact: graph titles are source-backed aliases,
    # not hand-written business synonyms. Exact graph-title questions use its view.
    if titles and chart_request:
        return [v for v in views if v.get('chart_title',v['metric']) in titles]
    aliases = {}
    for v in views:
        aliases.setdefault(v['source_series_id'], set()).add(v['metric'])
    for s in base:
        titles = sorted(t for t in aliases.get(s['series_id'], set()) if t != s['metric'])
        if titles:
            s['chart_titles'] = titles
    return base
