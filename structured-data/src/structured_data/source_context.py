"""Bounded, read-only header and formula provenance for same-name source columns.

This describes references. It never calculates formulas or assigns business
semantics absent from the source. Existing immutable datasets are not rewritten.
"""
from collections import defaultdict
from hashlib import file_digest, sha256
import json
from pathlib import Path
import posixpath
import re
import tempfile
import time
import xml.etree.ElementTree as ET
import zipfile

from openpyxl.formula import Tokenizer
from openpyxl.utils import get_column_letter, column_index_from_string

VERSION = 'source-context-v1'
NS = {'s': 'http://schemas.openxmlformats.org/spreadsheetml/2006/main',
      'r': 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'}
CELL = re.compile(r'^\$?([A-Z]{1,3})\$?([1-9][0-9]*)$')


def formula_references(formula, sheet):
    refs = []
    try:
        tokens = Tokenizer(formula).items
    except Exception:
        return refs
    for token in tokens:
        if token.type != 'OPERAND' or token.subtype != 'RANGE':
            continue
        raw = token.value
        if '[' in raw or ']' in raw:  # external workbooks: describe no external lookup
            continue
        target, reference = (raw.rsplit('!', 1) if '!' in raw else (sheet, raw))
        target = target.strip("'").replace("''", "'")
        ends = reference.upper().split(':')
        points = [CELL.fullmatch(end) for end in ends]
        if len(points) not in (1, 2) or any(p is None for p in points):
            continue
        first, last = column_index_from_string(points[0][1]), column_index_from_string(points[-1][1])
        if not 1 <= first <= last <= 16384 or last - first >= 8:
            continue
        item = {'sheet': target, 'reference': reference,
                'columns': [get_column_letter(c) for c in range(first, last + 1)]}
        if item not in refs:
            refs.append(item)
        if len(refs) == 8:
            break
    return refs


def _column(series):
    explicit = series.get('dimensions', {}).get('原表列')
    if explicit:
        return explicit
    cells = [CELL.fullmatch(str(c).upper()) for c in series.get('header_cells', [])]
    columns = {m[1] for m in cells if m}
    return next(iter(columns)) if len(columns) == 1 else None


def _header_rows(series):
    return {int(m[2]) for item in series for cell in item.get('header_cells', [])
            if (m := CELL.fullmatch(str(cell).upper())) and int(m[2]) <= 128} | {1, 2}


def _headers(archive, wanted, rows_by_sheet, deadline):
    workbook = ET.fromstring(archive.read('xl/workbook.xml'))
    rels = {r.get('Id'): r for r in ET.fromstring(archive.read('xl/_rels/workbook.xml.rels'))}
    result, shared_ids = {}, set()
    for sheet in workbook.findall('s:sheets/s:sheet', NS):
        name = sheet.get('name')
        if name not in wanted:
            continue
        rel = rels.get(sheet.get('{'+NS['r']+'}id'))
        if rel is None or rel.get('TargetMode') == 'External':
            continue
        path = posixpath.normpath(posixpath.join('xl', rel.get('Target', '').lstrip('/'))) if not rel.get('Target', '').startswith('/') else rel.get('Target').lstrip('/')
        if not path.startswith('xl/') or path not in archive.namelist():
            continue
        rows = rows_by_sheet.get(name, {1, 2})
        cells = {}
        with archive.open(path) as stream:
            for _, element in ET.iterparse(stream, events=('end',)):
                if time.monotonic() > deadline:
                    raise ValueError('原表字段上下文读取超时，请缩小工作表范围；未选择重复列')
                if element.tag != '{'+NS['s']+'}row':
                    continue
                rn = int(element.get('r', '0'))
                if rn > max(rows):
                    break
                if rn in rows:
                    for c in element.findall('s:c', NS):
                        cell = c.get('r', '')
                        if not (point := CELL.fullmatch(cell)) or column_index_from_string(point[1]) > 512:
                            continue
                        value = c.find('s:v', NS)
                        if c.get('t') == 'inlineStr':
                            text = ''.join(t.text or '' for t in c.findall('.//s:t', NS))
                        elif c.get('t') == 's' and value is not None:
                            text = {'shared': int(value.text)}
                            shared_ids.add(text['shared'])
                        else:
                            text = value.text if value is not None else None
                        if text is not None:
                            cells[cell] = text
                element.clear()
        # Merged header anchors are usually near the end of worksheet XML. Zip
        # seeking decompresses without constructing all data rows/cells in memory.
        with archive.open(path) as stream:
            size = archive.getinfo(path).file_size
            stream.seek(max(0, size - 262144))
            tail = stream.read(262144).decode('utf-8', errors='replace')
        merged = []
        for ref in re.findall(r'<(?:\w+:)?mergeCell\b[^>]*\bref="([A-Z0-9:]+)"', tail)[:256]:
            ends = ref.split(':');a, b = CELL.fullmatch(ends[0]), CELL.fullmatch(ends[-1])
            if a and b and int(a[2]) in rows and int(b[2]) <= 128:
                merged.append({'range': ref, 'anchor': ends[0], 'first_column': column_index_from_string(a[1]), 'last_column': column_index_from_string(b[1])})
        result[name] = {'cells': cells, 'merged': merged}
    shared = {}
    if shared_ids and 'xl/sharedStrings.xml' in archive.namelist():
        index = 0
        with archive.open('xl/sharedStrings.xml') as stream:
            for _, element in ET.iterparse(stream, events=('end',)):
                if time.monotonic() > deadline:
                    raise ValueError('原表表头读取超时，未选择重复列')
                if element.tag == '{'+NS['s']+'}si':
                    if index in shared_ids:
                        shared[index] = ''.join(t.text or '' for t in element.findall('.//s:t', NS))[:160]
                    element.clear();index += 1
                    if index > max(shared_ids):
                        break
    for sheet in result.values():
        sheet['cells'] = {cell: (shared.get(value['shared'], '') if isinstance(value, dict) else str(value))[:160] for cell, value in sheet['cells'].items()}
    return result


def enrich_catalog(store, dataset, catalog):
    groups = defaultdict(list)
    for item in catalog:
        if not item.get('chart_id'):
            groups[(item['sheet'], item['metric'])].append(item)
    targets = [s for values in groups.values() if len({_column(s) for s in values} - {None}) > 1 for s in values]
    if not targets:
        return catalog
    path = store._path(dataset) / 'source.xlsx'
    if not path.is_file():
        return catalog
    manifest = store.manifest(dataset);stat = path.stat()
    scope = sha256(json.dumps([(s['series_id'],s.get('header_cells'),s.get('dimensions')) for s in catalog],ensure_ascii=False,sort_keys=True).encode()).hexdigest()[:12]
    stamp = [VERSION, manifest['sha256'], stat.st_size, stat.st_mtime_ns, scope]
    cache_dir = store.root / 'source-context';cache_dir.mkdir(exist_ok=True)
    cache_path = cache_dir / (dataset + '-' + scope + '.json')
    cached = None
    try:
        cached = json.loads(cache_path.read_text())
    except (OSError, ValueError):
        pass
    if cached and cached.get('stamp') == stamp:
        contexts = cached['contexts']
    else:
        with path.open('rb') as source:
            if file_digest(source, 'sha256').hexdigest() != manifest['sha256']:
                raise ValueError('原文件与数据集指纹不一致，未读取重复列上下文')
        # Include unique dependency fields as well. The catalogue is already
        # bounded metadata; at most one representative formula per real series.
        sources = [s for s in catalog if not s.get('chart_id')][:2048]
        lookup = {(s['sheet'], _column(s)): s for s in sources if _column(s)}
        ids = [s['series_id'] for s in sources]
        formula_samples = {}
        with store._connect(dataset) as con:
            if ids:
                rows = con.execute("SELECT series_id,arg_min(struct_pack(cell:=source_cell,formula:=formula),struct_pack(missing:=value IS NULL,date:=date,cell:=source_cell)) FILTER(WHERE formula IS NOT NULL AND length(formula)<=400 AND regexp_matches(formula,'[A-Za-z][$]?[0-9]')) FROM observations WHERE series_id IN ("+','.join('?' for _ in ids)+") GROUP BY series_id", ids).fetchall()
                formula_samples = {sid: sample for sid, sample in rows if sample}
        wanted = {s['sheet'] for s in targets}
        for s in sources:
            sample = formula_samples.get(s['series_id'])
            if sample and s['sheet'] in wanted:
                wanted.update(r['sheet'] for r in formula_references(sample['formula'], s['sheet']))
        rows_by_sheet = {name: _header_rows([s for s in sources if s['sheet'] == name]) for name in wanted}
        with zipfile.ZipFile(path) as archive:
            headers = _headers(archive, wanted, rows_by_sheet, time.monotonic() + 20)
        def column_headers(sheet, column):
            header = headers.get(sheet, {});number = column_index_from_string(column)
            items = [{'cell': c, 'value': v} for c, v in header.get('cells', {}).items() if CELL.fullmatch(c)[1] == column]
            for merged in header.get('merged', []):
                value = header.get('cells', {}).get(merged['anchor'])
                if value and merged['first_column'] <= number <= merged['last_column']:
                    item = {'cell': merged['anchor'], 'value': value, 'merged_range': merged['range']}
                    if item not in items:items.append(item)
            return items[:12]
        def describe(s, depth=0, visited=()):
            column = _column(s);sid = s['series_id']
            description = {'source_column': column, 'headers': column_headers(s['sheet'], column) if column else []}
            sample = formula_samples.get(sid)
            if sample and depth < 3 and sid not in visited:
                refs = []
                for r in formula_references(sample['formula'], s['sheet']):
                    fields = []
                    for col in r['columns']:
                        field = {'column': col, 'headers': column_headers(r['sheet'], col)}
                        dependency = lookup.get((r['sheet'], col))
                        if dependency:
                            field.update(series_id=dependency['series_id'], metric=dependency['metric'], unit=dependency.get('unit'))
                            if depth < 2 and dependency['series_id'] not in (*visited, sid):
                                field['source_context'] = describe(dependency, depth+1, (*visited, sid))
                        fields.append(field)
                    refs.append({**r, 'fields': fields})
                if refs:description['formula_source'] = {**sample, 'references': refs}
            return description
        contexts = {s['series_id']: {**describe(s), 'source_sha256': manifest['sha256'], 'notice': '表头与公式引用来自保留的原文件及已保存公式；不执行公式，不推定业务含义、自然日或交易日，不因同名认定同源。'} for s in targets[:512]}
        with tempfile.NamedTemporaryFile('w', dir=cache_dir, delete=False) as output:
            json.dump({'stamp': stamp, 'contexts': contexts}, output, ensure_ascii=False);temporary = Path(output.name)
        temporary.replace(cache_path)
    return [{**s, **({'source_context': contexts[s['series_id']]} if s['series_id'] in contexts else {})} for s in catalog]


def protect_duplicate_bindings(proposal, catalog, question, confirmed_ids=()):
    """A model's singleton cannot erase literal same-header source ambiguity.

    Only explicit source labels/columns from this request, or an actual prior
    human selection, can narrow the candidates. Negated sources are left to the
    model as multiple candidates; they cannot serve as positive evidence here.
    """
    import unicodedata
    def normalize(text):
        return re.sub(r'\s+', '', unicodedata.normalize('NFKC', str(text)).lower())
    text = normalize(question)
    lookup = {s['series_id']: s for s in catalog}
    groups = defaultdict(list)
    for s in catalog:
        if not s.get('chart_id') and s.get('source_context'):
            groups[(s['sheet'], s['metric'])].append(s)
    for field in proposal.get('fields', []):
        candidates = field.get('candidate_ids', field.get('candidates', []))
        ids = [c.get('id') if isinstance(c, dict) else c for c in candidates]
        if len(ids) != 1 or ids[0] not in lookup:
            continue
        selected = lookup[ids[0]]
        siblings = groups.get((selected['sheet'], selected['metric']), [])
        if len(siblings) < 2 or normalize(selected['metric']) not in text:
            continue
        chosen = [s for s in siblings if s['series_id'] in confirmed_ids]
        if len(chosen) == 1:
            continue
        columns = [s for s in siblings if re.search(r'(?<![a-z])'+re.escape(_column(s).lower())+r'列', text)]
        negated = re.search(r'不要|不是|不取|排除|不用|除外|非', text)
        matches = columns if len(columns) == 1 and not negated else []
        if not matches and not negated:
            def labels(context, depth=0):
                found = []
                # Merged parent headings are positive source labels as well.
                found.extend((h['value'], max(1, 3-depth)) for h in context.get('headers', []) if h.get('merged_range'))
                for ref in context.get('formula_source', {}).get('references', []):
                    for source in ref['fields']:
                        source_labels = {source.get('metric', ''), *(h['value'] for h in source.get('headers', []))}
                        found.extend((label, max(1, 3-depth)) for label in source_labels if label)
                        found.extend(labels(source.get('source_context', {}), depth+1))
                return found
            scores = {}
            shared_labels = {normalize(selected['metric']), normalize(selected.get('unit') or '')}
            for s in siblings:
                matched = {}
                for label, weight in labels(s['source_context']):
                    label = normalize(label)
                    if label in shared_labels or len(label) < 3:
                        continue
                    # Match a complete source label or a substantial literal
                    # prefix ('双29' / 'CC Index'); never model-generated aliases.
                    if label in text:
                        matched[label] = max(matched.get(label, 0), weight * 10)
                    elif any(label[:n] in text for n in range(3, len(label))):
                        matched[label] = max(matched.get(label, 0), weight)
                scores[s['series_id']] = sum(matched.values())
            best = max(scores.values(), default=0)
            if best:
                matches = [s for s in siblings if scores[s['series_id']] == best]
        choices = matches or siblings
        if len(choices) == 1 and choices[0]['series_id'] == ids[0]:
            continue
        field['candidate_ids'] = [s['series_id'] for s in choices]
        field.pop('candidates', None)
        field.pop('equivalent_source_ids', None)
        field['reason'] = '同名字段按实际表头及公式来源区分；用户口径未唯一匹配时保留真实备选。'
        if len(choices) > 1:
            proposal['decision'] = 'clarify'
            proposal['question'] = '这些同名列引用了不同的原料来源，请明确需要哪一种来源口径。'
    return proposal


def verify_selection(store, dataset, series_ids, question):
    """Bound user wording supplied by the signed application, not a model claim."""
    if not question:
        return None
    if not isinstance(question, str) or len(question) > 6000:
        raise ValueError('来源口径问题无效')
    catalog = store.catalog(dataset)
    fields = []
    requested = {s['series_id']: s for s in catalog}
    for sid in series_ids:
        if sid not in requested:
            continue  # the normal query path still validates actual IDs
        proposal = {'fields': [{'role': 'v1', 'candidate_ids': [sid]}]}
        protect_duplicate_bindings(proposal, catalog, question)
        choices = proposal['fields'][0]['candidate_ids']
        if choices != [sid]:
            fields.append({'selected_series_id': sid, 'metric': requested[sid]['metric'],
                           'candidates': [{k: requested[c].get(k) for k in ('series_id', 'sheet', 'metric', 'unit', 'dimensions', 'source_context')} for c in choices]})
    if fields:
        return {'kind': 'structured_source_ambiguity', 'status': 'needs_clarification',
                'fields': fields, 'notice': '当前问题未唯一确定同名列的来源口径，尚未执行数值查询。请依据这些真实表头和公式来源澄清；不能代替用户选择、混合相加或宣称无数据。'}
    return None


def source_description(context):
    """Human-readable actual inputs, retaining separate formula branches."""
    def inputs(node,depth=0):
        found=[]
        for ref in node.get('formula_source',{}).get('references',[]):
            for field in ref.get('fields',[]):
                metric=field.get('metric')
                if not metric:continue
                children=inputs(field.get('source_context',{}),depth+1) if depth<2 else []
                label=metric+('（引用'+'、'.join(children)+'）' if children else '')
                if label not in found:found.append(label)
        return found[:8]
    sources=inputs(context);formula=context.get('formula_source',{})
    if not sources:return ''
    return '原表 '+str(context.get('source_column') or '')+' 列；公式引用来源：'+'、'.join(sources)+('；公式样本 '+formula.get('cell','')+' '+formula.get('formula','') if formula.get('formula') else '')+'。该样本不代表整列公式始终一致。'
