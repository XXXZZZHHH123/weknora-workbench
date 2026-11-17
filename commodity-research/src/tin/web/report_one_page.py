"""One-page report projection from explicit bindings and calculated observations.

Ordering here selects display facts only. It never assigns business meaning or
an impact; those are saved researcher choices or unaccepted model suggestions.
"""
from copy import deepcopy
from math import isfinite
import re

from tin.web.number_format import format_number, source_decimals
from tin.web.research_dimensions import default_dimensions, read_dimensions, summaries
from tin.web.research_display import indicator_name

IMPACTS = {'bullish': '偏多', 'bearish': '偏空', 'neutral': '中性', 'undecided': '未判断'}
UNLABELED, DELETED, OTHER = '__unlabeled__', '__deleted__', '__other__'


def _items(dimension):
    return [item for section in dimension.get('sections') or [] for item in section.get('items') or []]


def _compact(card):
    result = {key: deepcopy(value) for key, value in card.items()
              if key not in {'points', 'payload', 'definition'}}
    if 'indicators' in result:
        result['indicators'] = [_compact(item) for item in result['indicators']]
    return result


def attach_dimension_data(session, payload, scope):
    """Read exactly the dimension board's cutoff/period, never for archives or restricted views."""
    if not session.info.get('workbench_id') or scope is not None and scope.restricted:
        return payload
    dimensions = read_dimensions(session)
    payload['research_dimensions'] = summaries(dimensions)
    from tin.web.dimension_board import revision
    config_revision = revision(session)
    if any(_items(dimension) for dimension in dimensions):
        from tin.web.dimension_data import dimension_data
        period = payload.get('period') or {}
        data = dimension_data(scope, dimensions, period.get('end') or payload.get('as_of'),
                              period.get('kind') or 'daily')
        data = deepcopy(data)
        for dimension in data['dimensions']:
            for section in dimension.get('sections') or []:
                section['items'] = [_compact(card) for card in section.get('items') or []]
        payload['dimension_data'] = data
    else:
        payload['dimension_data'] = {'dimensions': dimensions, 'failed': [], 'warnings': []}
    payload['dimension_data']['revision'] = config_revision
    metadata = {}
    for dimension in payload['dimension_data']['dimensions']:
        for item in _items(dimension):
            for card in ([item] if item.get('kind') == 'indicator' else item.get('indicators') or []):
                if card.get('status') == 'ok':
                    metadata[card.get('series_id') or card.get('id')] = card
    for figure in payload.get('report_figures') or []:
        if figure.get('kind') == 'indicator' and figure.get('series_id') in metadata:
            card = metadata[figure['series_id']]
            figure.update(title=card['name'], unit=card.get('unit') or '')
    return payload


def _number(value):
    return type(value) in {int, float} and isfinite(value)


def _no_update(card):
    return card.get('update_status') == 'no_update' or card.get('direction') == 'no_update'


def _numeric_card(card):
    # Roles come from the authorized structured directory, never label guesses.
    role = (card.get('field_metadata') or {}).get('role') or card.get('role')
    return role not in {'annotation', 'text_dimension'} and card.get('status') == 'ok' and _number(card.get('latest'))


def _cards(dimension, day=None):
    seen = set()
    for item in _items(dimension):
        # A historical chart remains evidence for its own window only.
        if item.get('kind') == 'chart' and day and item.get('as_of') and item['as_of'] < day:
            continue
        for card in ([item] if item.get('kind') == 'indicator' else item.get('indicators') or []):
            sid = card.get('series_id') or card.get('id')
            if sid not in seen and _numeric_card(card):
                seen.add(sid)
                yield card


def _readable_name(card):
    sid = card.get('series_id') or card.get('id') or ''
    name = indicator_name(sid, {sid: card.get('readable_name') or card.get('name') or card.get('raw_name')})
    # A slash-separated ASCII source prefix is metadata beside a readable
    # localized label. Keep the original name for source and model verification.
    parts = re.split(r'\s+[/／]\s+', name)
    if len(parts) > 1 and parts[0].isascii() and any(not part.isascii() for part in parts[1:]):
        name = ' / '.join(parts[1:])
    return name


def _fact(card):
    display = card.get('display') or {}
    baseline = card.get('baseline') or {}
    position = card.get('position') or {}
    latest = display.get('latest') or format_number(card['latest'], decimals=source_decimals(card))
    change = display.get('change') or format_number(card.get('change'), decimals=source_decimals(card), signed=True)
    details = []
    if _no_update(card):
        latest_date = card.get('date') or card.get('latest_date')
        details.append('本期无新数据' + ('，最新 ' + latest_date if latest_date else ''))
    elif baseline.get('date') and _number(card.get('change')):
        details.append('较 ' + baseline['date'][5:] + ' ' + change)
    if position.get('rank') is not None and position.get('n', 0) >= 3:
        details.append(position.get('text') or '')
    name = _readable_name(card)
    unit = str(card.get('unit') or '').strip()
    if unit == '未注明':
        unit = ''
    latest_text = name + ' ' + latest + (' ' + unit if unit else '')
    text = latest_text
    if details:
        text += '（' + '，'.join(filter(None, details)) + '）'
    return {**_compact(card), 'name': name, 'readable_name': name,
            'raw_name': card.get('raw_name') or card.get('name') or '', 'unit': unit,
            'latest_text': latest_text, 'text': text}


def select_facts(dimension, limit=4, *, day=None):
    """Prefer updates, then seasonal extremes and absolute observed change rate."""
    cards = list(_cards(dimension, day))

    def rank(card):
        position = card.get('position') or {}
        extreme = position.get('n', 0) >= 3 and any(
            _number(position.get(key)) and position[key] <= 2 for key in ('rank', 'rank_low'))
        pct = card.get('change_pct')
        return (_no_update(card), not extreme, -abs(pct) if _number(pct) else float('inf'))

    if cards and all(_no_update(card) for card in cards):
        limit = min(limit, 2)
    return [_fact(card) for card in sorted(cards, key=rank)[:limit]]


def _dimension_id(identifier, valid):
    return identifier if identifier in valid else DELETED if identifier else UNLABELED


def dimension_overview(payload, dimensions=None):
    dimensions = dimensions if dimensions is not None else payload.get('research_dimensions') or default_dimensions()
    data = {row['id']: row for row in dimensions}
    data.update((row['id'], row) for row in (payload.get('dimension_data') or {}).get('dimensions') or [])
    saved = ((payload.get('manual_report') or {}).get('draft') or {})
    notes = saved.get('dimension_notes') or {}
    day = (payload.get('period') or {}).get('end') or payload.get('as_of')
    rows = {dimension['id']: {'id': dimension['id'], 'label': dimension['label'],
        'facts': select_facts(data.get(dimension['id']) or dimension, day=day),
        'has_binding': bool(_items(data.get(dimension['id']) or dimension)),
        'evidence': [], 'support_count': 0, 'oppose_count': 0} for dimension in dimensions}
    valid = set(rows)
    contradiction = (payload.get('judgment') or {}).get('contradiction')
    if isinstance(contradiction, dict):
        for side, count in (('bull', 'support_count'), ('bear', 'oppose_count')):
            for item in (contradiction.get(side) or {}).get('evidence') or []:
                if not isinstance(item, dict):
                    continue
                identifier = _dimension_id(item.get('dimension'), valid)
                row = rows.setdefault(identifier, {'id': identifier,
                    'label': '未标注' if identifier == UNLABELED else '已删除维度',
                    'facts': [], 'has_binding': False, 'evidence': [], 'support_count': 0, 'oppose_count': 0})
                row['evidence'].append({**deepcopy(item), 'side': side, 'text': str(item.get('desc') or '')})
                row[count] += 1
    for identifier, note in notes.items():
        # Preserve a researcher's paragraph when its dimension was later deleted.
        key = _dimension_id(identifier, valid)
        if key not in rows:
            rows[key] = {'id': key, 'label': '已删除维度', 'facts': [], 'has_binding': False,
                         'evidence': [], 'support_count': 0, 'oppose_count': 0}
        row = rows[key]
        if key == DELETED:
            row.setdefault('historical_notes', []).append({'dimension': identifier, **deepcopy(note)})
        if key != identifier and row.get('sentence'):
            row['sentence'] += '\n' + note.get('text', '')
        else:
            row['sentence'] = note.get('text') or ''
        row['impact'] = note.get('impact') if note.get('impact') in IMPACTS else 'undecided'
        if len(row.get('historical_notes') or []) > 1:
            row['impact'] = 'undecided'
    output = []
    for row in rows.values():
        row.setdefault('sentence', '')
        row.setdefault('impact', 'undecided')
        row['impact_label'] = IMPACTS[row['impact']]
        cards = list(_cards(data.get(row['id']) or {}, day))
        row['no_update'] = bool(cards) and all(_no_update(card) for card in cards)
        row['latest_date'] = max((card.get('date') or card.get('latest_date') or '' for card in cards), default='')
        row['change_summary'] = ('本期无新数据' + ('（最新 ' + row['latest_date'] + '）' if row['latest_date'] else '')) if row['no_update'] else ''
        warnings = [item.get('reason') for item in _items(data.get(row['id']) or {}) if item.get('status') == 'unavailable']
        if any(item.get('kind') == 'chart' and day and item.get('as_of') and item['as_of'] < day
               for item in _items(data.get(row['id']) or {})):
            warnings.append('历史区间图仅作证据，不将历史窗口的变化作为本期变化')
        stale = []
        for fact in cards:
            if (fact.get('freshness') or {}).get('stale'):
                stale.append(fact)
                warnings.append(f"{_readable_name(fact)}：最新数据距今 {fact['freshness']['age_days']} 天，可能已停更")
            if (fact.get('position') or {}).get('notice'):
                warnings.append(fact['position']['notice'])
            if (fact.get('history_coverage') or {}).get('limited', fact.get('limited', False)):
                warnings.append('历史观测达到读取上限，同期位置可能受限')
        row['warnings'] = list(dict.fromkeys(filter(None, warnings)))
        row['warning_summary'] = (f"{len(stale)} 项停更（最长 {max(fact['freshness']['age_days'] for fact in stale)} 天）"
                                  if stale else '')
        row['review_needed'] = row['id'] in valid and (
            (row['impact'] == 'bearish' and row['support_count'] > row['oppose_count'])
            or (row['impact'] == 'bullish' and row['oppose_count'] > row['support_count']))
        if row['has_binding'] or row['evidence'] or row['sentence'].strip() or row['impact'] != 'undecided':
            output.append(row)
    return output


def evidence_groups(payload, figures, dimensions):
    """Group each deduplicated figure once, using bindings before explicit citation tags."""
    valid = {row['id'] for row in dimensions}
    configured = (payload.get('dimension_data') or {}).get('dimensions') or dimensions
    chart_bindings, series_bindings = {}, {}
    for dimension in configured:
        for item in _items(dimension):
            if item.get('kind') == 'chart':
                chart_bindings.setdefault(item.get('id'), dimension['id'])
                for source in item.get('sources') or []:
                    series_bindings.setdefault(source.get('id'), dimension['id'])
            else:
                series_bindings.setdefault(item.get('id'), dimension['id'])
    groups = {row['id']: {'id': row['id'], 'label': row['label'], 'figures': []} for row in dimensions}
    labels = {UNLABELED: '未标注', DELETED: '已删除维度', OTHER: '其他'}
    conclusions = (((payload.get('manual_report') or {}).get('draft') or {}).get('figure_conclusions') or {})
    for original in figures:
        figure = deepcopy(original)
        reference = figure.get('reference') or {}
        identifier = chart_bindings.get(reference.get('id'))
        sids = [figure.get('series_id'), figure.get('single_series_id')]
        sids.extend(curve.get('series_id') for curve in (reference.get('definition') or {}).get('curves') or [])
        if identifier is None:
            matches = {series_bindings[sid] for sid in sids if sid in series_bindings}
            identifier = next((row['id'] for row in dimensions if row['id'] in matches), None)
        if identifier is None:
            tags = [row.get('dimension') for row in figure.get('references') or [] if row.get('dimension')]
            identifier = next((row['id'] for row in dimensions if row['id'] in tags), None)
            if identifier is None:
                identifier = DELETED if tags else OTHER
        group = groups.setdefault(identifier, {'id': identifier, 'label': labels.get(identifier, '其他'), 'figures': []})
        figure['dimension'] = identifier
        figure['dimension_label'] = group['label']
        figure['conclusion'] = conclusions.get(figure['key']) or ''
        group['figures'].append(figure)
    return [row for row in groups.values() if row['figures']]


def logic_strategy(view):
    judgment = view.get('judgment') or {}
    active = judgment.get('status') == '生效'
    conditions = [deepcopy(row) for kind in ('thresholds', 'falsifiers') for row in judgment.get(kind) or []] if active else []
    strategies = []
    for card in view.get('research_cards') or []:
        parts = [card.get('direction_label'), card.get('thesis'),
                 ('触发：' + card['trigger']) if card.get('trigger') else '',
                 ('应对：' + card['response_action']) if card.get('response_action') else '']
        strategies.append({'id': card.get('id'), 'title': card.get('title') or '',
                           'direction': card.get('direction_label') or '', 'thesis': card.get('thesis') or '',
                           'trigger': card.get('trigger') or '', 'response_action': card.get('response_action') or '',
                           'text': '；'.join(str(part) for part in parts if part)})
    for plan in view.get('strategies') or []:
        directions = ' / '.join(dict.fromkeys({'buy': '买入', 'sell': '卖出', 'long': '做多', 'short': '做空'}.get(
            leg.get('side'), leg.get('side') or '') for leg in plan.get('legs') or []))
        entry = plan.get('entry') or {}
        trigger = ' – '.join(str(entry[key]) for key in ('low_text', 'high_text') if entry.get(key))
        parts = [directions, plan.get('thesis'), ('触发区间：' + trigger) if trigger else '',
                 ('失效应对：' + plan['invalidation']) if plan.get('invalidation') else '']
        strategies.append({'id': plan.get('id'), 'title': plan.get('title') or '',
                           'direction': directions, 'thesis': plan.get('thesis') or '',
                           'trigger': trigger, 'response_action': plan.get('invalidation') or '',
                           'text': '；'.join(str(part) for part in parts if part)})
    return {'active': active, 'contradiction': judgment.get('contradiction') if active else '',
            'tone': ' · '.join(filter(None, (judgment.get('tone'), judgment.get('tone_note')))) if active else '',
            'conditions': conditions, 'strategies': strategies}


def present_one_page(payload, view, dimensions=None):
    dimensions = dimensions if dimensions is not None else payload.get('research_dimensions') or default_dimensions()
    groups = evidence_groups(payload, view['report_figures'], dimensions)
    by_key = {figure['key']: figure for group in groups for figure in group['figures']}
    overview_payload = deepcopy(payload)
    if not view['panels']['research']['visible']:
        overview_payload['judgment'] = None
    logic = logic_strategy(view)
    if not view['panels']['research']['visible']:
        logic.update(active=False, contradiction='', tone='', conditions=[])
    if not view['panels']['watch']['visible']:
        logic['strategies'] = []
    # Dimension bindings need not appear in the report page's bounded fact
    # window. Merge their authorized metadata without another source read.
    options = {row['series_id']: deepcopy(row) for row in view.get('all_figure_options') or []}
    for dimension in (payload.get('dimension_data') or {}).get('dimensions') or []:
        for item in _items(dimension):
            candidates = [item] if item.get('kind') == 'indicator' else item.get('indicators') or []
            for card in candidates:
                sid = card.get('series_id') or card.get('id')
                if sid and card.get('status') == 'ok':
                    options.setdefault(sid, {'series_id': sid, 'name': card.get('name') or sid,
                                             'unit': card.get('unit') or ''})
    excluded = {figure.get('single_series_id') for figure in view.get('automatic_figures') or []}
    return {'dimension_overview': dimension_overview(overview_payload, dimensions),
            'editable_dimensions': deepcopy(dimensions), 'research_dimensions': deepcopy(dimensions),
            'dimension_data': deepcopy(payload.get('dimension_data') or {}),
            'logic_strategy': logic, 'evidence_groups': groups,
            'all_figure_options': list(options.values()),
            'figure_options': [row for sid, row in options.items() if sid not in excluded],
            'report_figures': [by_key[figure['key']] for figure in view['report_figures']]}
