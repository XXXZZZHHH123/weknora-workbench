"""Recall evidence only: no candidate, including a numerical match, binds a slot."""
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP, localcontext
import unicodedata


def _grams(text):
    text = ''.join(c for c in unicodedata.normalize('NFKC', text).casefold() if c.isalnum())
    return {text[i:i + 2] for i in range(len(text) - 1)} if len(text) > 1 else set(text)


def chart_candidates(texts, charts):
    evidence = _grams(' '.join(texts))
    if not evidence:
        return []
    ranked = []
    for chart in charts:
        grams = _grams(' '.join([chart['title'], *chart['labels'], *chart['segment_labels'], *chart['units']]))
        score = 2 * len(evidence & grams) / (len(evidence) + len(grams)) if grams else 0
        if score:
            ranked.append({'kind': 'chart', 'chart_id': chart['id'], 'title': chart['title'],
                           'score': round(score, 6)})
    return sorted(ranked, key=lambda value: (-value['score'], value['chart_id']))[:3]


def verify_pairs(pairs, points):
    observations = {point['date']: point['value'] for point in points if point['value'] is not None}
    matched = 0
    with localcontext() as context:
        context.prec = 256
        for pair in pairs:
            actual = observations.get(pair['date'])
            if actual is None:
                continue
            try:
                actual = Decimal(str(actual))
                expected = Decimal(pair['value'])
                quantum = Decimal(1).scaleb(-pair['decimals'])
                if actual.is_finite() and actual.quantize(quantum, rounding=ROUND_HALF_UP) == expected:
                    matched += 1
            except (InvalidOperation, ValueError):
                continue
    total = len(pairs)
    return {'matched': matched, 'total': total, 'score': matched / total if total else 0,
            'numerically_verified': bool(total and matched * 5 >= total * 4)}


def template_candidates(presentation, bindings, charts, fields, read_series):
    """The caller supplies permission-checked charts and the existing bounded reader."""
    by_id = {chart['id']: chart for chart in charts}
    result = {}
    cache = {}
    for slide in presentation['slides']:
        page_charts = set()
        for slot in slide['slots']:
            sid = slot['slot_id']
            candidates = chart_candidates(slot['texts'], charts) if slot['kind'] == 'chart' else []
            result[sid] = candidates
            page_charts.update(value['chart_id'] for value in candidates)
            binding = bindings.get(sid)
            if binding and binding['kind'] == 'chart' and binding['chart_id'] in by_id:
                page_charts.add(binding['chart_id'])
        identifiers = sorted({sid for cid in page_charts for sid in by_id[cid]['series_ids'] if sid in fields})
        for slot in slide['slots']:
            if slot['kind'] != 'table' or not identifiers or not slot['pairs']:
                continue
            day = max(pair['date'] for pair in slot['pairs'])
            key = (tuple(identifiers), day)
            if key not in cache:
                from datetime import date
                cache[key] = read_series(identifiers, date.fromisoformat(day), limit=2000)
            candidates = []
            for sid in identifiers:
                series = cache[key].get(sid)
                if series is None:
                    continue
                evidence = verify_pairs(slot['pairs'], series['points'])
                if evidence['numerically_verified']:
                    candidates.append({'kind': 'series', 'series_id': sid,
                        'name': fields[sid].get('metric') or fields[sid].get('name', ''),
                        'rows': min(60, len(slot['pairs'])), 'label': '数值已核实',
                        'observation_window_limited': series['limited'], **evidence})
            result[slot['slot_id']] = sorted(candidates, key=lambda value: (-value['score'], value['series_id']))
    return result
