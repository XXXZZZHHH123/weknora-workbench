"""Report chart manifest, derived exclusively from the displayed saved records.

Building the list never reads observations. Each chart's data is read separately
through chart_references, with its original revision and the report's cutoff.
"""
from copy import deepcopy
from urllib.parse import quote

from fastapi import HTTPException
from tin.web.research_display import human_text


FIELD_LABELS = {'thesis': '适用范围与目标', 'watch_for': '执行前核对', 'trigger': '适用 / 触发',
                'response_action': '应对措施', 'invalidation': '暂停 / 撤回', 'original_text': '原文'}


def target_text(reference, record):
    target = reference.get('target') or {}
    kind = target.get('kind')
    if kind == 'evidence':
        side, index = target.get('side'), target.get('index')
        label = {'bull': '支持依据', 'bear': '反对依据'}.get(side, '依据')
        contradiction = record.get('contradiction') or {}
        evidence = (contradiction.get(side) or {}).get('evidence') or []
        if type(index) is int and 0 <= index < len(evidence):
            return label + '「' + str(evidence[index].get('desc') or '') + '」'
        return label
    if kind in {'threshold', 'falsifier'}:
        label = '区间与阈值' if kind == 'threshold' else '证伪条件'
        rows = record.get('thresholds' if kind == 'threshold' else 'falsifiers') or []
        item = next((row for row in rows if row.get('id') == target.get('id')), {})
        return label + '「' + str(item.get('name') or item.get('desc') or target.get('id') or '') + '」'
    return FIELD_LABELS.get(reference.get('field'), '')


def record_chart_references(record):
    """Also used for exact-version report quotations; no names select content."""
    contradiction = record.get('contradiction')
    pins = ((contradiction.get('chart_references') or []) if isinstance(contradiction, dict) else [])
    pins = [*pins, *(record.get('chart_references') or []),
            *(row for row in record.get('citations') or [] if row.get('kind') == 'chart')]
    return [{**deepcopy(pin), 'report_target_text': pin.get('report_target_text') or target_text(pin, record),
             **({'dimension': target_dimension(pin, record)} if target_dimension(pin, record) else {})} for pin in pins]


def target_dimension(reference, record):
    """Only the explicitly tagged item at the saved citation target supplies a dimension."""
    target = reference.get('target') or {}
    kind = target.get('kind')
    if kind == 'evidence':
        contradiction = record.get('contradiction') or {}
        rows = ((contradiction.get(target.get('side')) or {}).get('evidence') or []) if isinstance(contradiction, dict) else []
        index = target.get('index')
        if type(index) is int and 0 <= index < len(rows):
            return rows[index].get('dimension')
    elif kind in {'threshold', 'falsifier'}:
        rows = record.get('thresholds' if kind == 'threshold' else 'falsifiers') or []
        return next((row.get('dimension') for row in rows if row.get('id') == target.get('id')), None)
    return reference.get('dimension')


def _sections(payload):
    manual = payload.get('manual_report') or {}
    draft = manual.get('draft') or {}
    return draft, {key: (draft.get('sections') or {}).get(key, {}).get('visible', True)
                   for key in ('research', 'watch')}


def collect_figures(payload, *, restricted=False):
    draft, visible = _sections(payload)
    through = (payload.get('period') or {}).get('end') or payload.get('as_of')
    records = []
    if not restricted:
        if visible['research']:
            records.append(('judgment', '', payload.get('judgment') or {}))
            records.extend(('judgment', '', row) for row in draft.get('reference_snapshots') or []
                           if row.get('kind') == 'judgment')
        if visible['watch']:
            records.extend(('strategy', row.get('title') or '', row)
                           for row in [*(payload.get('research_cards') or []), *(payload.get('strategies') or [])])
            records.extend(('strategy', row.get('title') or '', row) for row in draft.get('reference_snapshots') or []
                           if row.get('kind') == 'strategy')
    charts = {}
    for kind, title, record in records:
        for pin in record_chart_references(record):
            identifier = pin.get('id')
            if not isinstance(identifier, str) or type(pin.get('revision')) is not int:
                continue
            location = '研判' if kind == 'judgment' else '策略「' + title + '」'
            target = pin.get('report_target_text') or target_text(pin, record)
            curves = (pin.get('definition') or {}).get('curves') or []
            occurrence = {'kind': kind, 'title': title, 'target': deepcopy(pin.get('target')),
                          'chart_revision': pin['revision'], 'chart_title': pin.get('title') or '引用图表',
                          'single_series_id': curves[0].get('series_id') if len(curves) == 1 else None,
                          'target_text': target, 'text': location + ('（' + target + '）' if target else '')}
            if pin.get('dimension'):
                occurrence['dimension'] = pin['dimension']
            if record.get('kind') in {'judgment', 'strategy'} and record.get('id') is not None:
                occurrence['report_reference'] = {key: record.get(key) for key in ('kind', 'id', 'revision')}
            if identifier not in charts:
                charts[identifier] = {'reference': pin, 'references': []}
            item = charts[identifier]
            if pin['revision'] > item['reference']['revision']:
                item['reference'] = pin
            if occurrence not in item['references']:
                item['references'].append(occurrence)
    result, duplicated = [], set()
    for identifier, item in charts.items():
        pin = item['reference']
        curves = (pin.get('definition') or {}).get('curves') or []
        single = curves[0].get('series_id') if len(curves) == 1 else None
        if single:
            duplicated.add(single)
        key = 'chart:' + identifier
        window = (pin.get('definition') or {}).get('time') or {}
        as_of = min(through, window['end']) if through and window.get('mode') == 'range' and window.get('end') else through
        result.append({'key': key, 'kind': 'chart', 'title': pin.get('title') or '引用图表',
            'caption': '引用于：' + '、'.join(dict.fromkeys(row['text'] for row in item['references'])),
            'references': item['references'], 'reference': deepcopy(pin), 'single_series_id': single,
            'unit': '', 'as_of': as_of, 'render_url': '/api/sn/reports/figures/' + quote(key, safe='')})
    observations = {row['series_id']: row for row in payload.get('observations') or []}
    chosen = draft.get('figures') if draft else None
    if chosen is None:
        ids = payload.get('report_figure_selection')
        if not isinstance(ids, list):
            ids = list(observations)[:6]
        chosen = [{'series_id': sid, 'caption': ''} for sid in ids[:6]]
    for selected in chosen:
        sid = selected['series_id']
        if sid in duplicated:
            continue
        row = observations.get(sid) or {}
        key = 'indicator:' + sid
        result.append({'key': key, 'kind': 'indicator', 'series_id': sid,
            'title': human_text(row.get('name')) or '已引用指标', 'caption': selected.get('caption') or '',
            'unit': row.get('unit') or '', 'as_of': through,
            'render_url': '/api/sn/reports/figures/' + quote(key, safe='')})
    return result


def attach_figures(payload, scope, *, metadata=True):
    restricted = scope is not None and scope.restricted
    figures = collect_figures(payload, restricted=restricted)
    if metadata and not restricted:
        # Resolve graph ACL and exact revisions before reading any extra field
        # metadata. Units are metadata only; observations remain lazy.
        from tin.web.chart_references import reference_definition, _metadata
        from tin.web.chart_data import definition_series_ids, curve_unit
        definitions, identifiers = {}, []
        for figure in figures:
            if figure['kind'] != 'chart':
                continue
            try:
                _, definition, _ = reference_definition(scope, figure['reference'])
                definitions[figure['key']] = definition
                identifiers.extend(definition_series_ids(definition))
                figure['title'] = definition.title
            except HTTPException as exc:
                figure['notice'] = str(exc.detail)
        fields = {}
        if identifiers:
            try:
                fields, _ = _metadata(scope, identifiers)
            except HTTPException as exc:
                for figure in figures:
                    if figure['key'] in definitions:
                        figure['notice'] = str(exc.detail)
        for figure in figures:
            definition = definitions.get(figure['key'])
            if definition is not None and fields:
                figure['unit'] = ' / '.join(dict.fromkeys(curve_unit(curve, fields) for curve in definition.curves))
    payload['report_figures'] = figures
    return payload
