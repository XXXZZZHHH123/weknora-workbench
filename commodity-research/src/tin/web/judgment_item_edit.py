"""Read a saved judgment into review and project explicit decisions back onto it."""

from collections import Counter
from copy import deepcopy
from decimal import Decimal
import secrets

from pydantic import ValidationError
from sqlalchemy import select

from tin.compute.rule_caliber import unit_present
from tin.judgments.analysis import ALLOWED_FIELDS, AnalysisError
from tin.judgments.markdown import dump_markdown
from tin.models import AuditLog
from tin.schemas.judgment import EXPRESSION, JudgmentIn, binding_ids
from tin.web import judgment_quantity as quantity

SAVE_ACTION = '研判逐条修改保存'
_ANALYSIS_SAVE = '研究逻辑分析保存'
_DIMENSION_SAVE = '研判维度保存'
_REVIEW_STATUSES = {'pending', 'ready', 'confirmed', 'kept_text'}
_NOTE_REASONS = {
    'rule_kept': '已保留为文字，未作为量化规则执行。',
    'rule_pending': '待核对原文、数字、单位和指标；当前不作为量化规则执行。',
    'whitelist': '仅保留指标说明，不列为绑定指标。',
    'description': '用户保留为文字说明。',
}
_DESCRIPTION_ENUMS = {'type', 'category', 'paradigm', 'tone'}


def read_saved_audit(session, row):
    """Old analysis saves retain decisions in the audit, outside the typed payload."""
    source_id = str(row.id)
    visited = set()
    while source_id not in visited:
        visited.add(source_id)
        log = session.execute(select(AuditLog.action, AuditLog.detail).where(
            AuditLog.target_type == 'judgment', AuditLog.target_id == source_id,
            AuditLog.action.in_((SAVE_ACTION, _ANALYSIS_SAVE, _DIMENSION_SAVE)),
        ).order_by(AuditLog.id.desc()).limit(1)).first()
        if log is None:
            return {}
        action, detail = log
        if action != _DIMENSION_SAVE:
            return detail
        source_id = str(detail.get('source_id'))
    return {}


def _item_key(value):
    from tin.web.judgment_review import digest

    if isinstance(value, dict):
        value = {key: field for key, field in value.items() if key != 'dimension'}
    return digest(value)


def _same_legacy_item(item, value, decision):
    if item['kind'] == 'whitelist':
        return value == decision.get('series_id', item['series_id'])
    if item['kind'] in quantity.RULE_KINDS:
        return (isinstance(value, dict) and value['id'] == item['value']['id']
                and value['series_id'] == decision.get('series_id', item['series_id']))
    if isinstance(item['value'], dict):
        if not isinstance(value, dict):
            return False
        original = item['value']
        if item['kind'] == 'unresolved_binding':
            text = original['text']
            condition = original.get('business_condition')
            texts = {text}
            if condition and condition not in text and value.get('business_condition') is None:
                texts.add(text + '\n\n' + condition if text else condition)
            return value['section'] == original['section'] and value['text'] in texts
        if item['path'] == ['contradiction', 'window']:
            return all(value.get(key) == field for key, field in (
                ('from', original.get('from', original.get('start'))),
                ('to', original.get('to')), ('note', original.get('note'))))
        # These descriptions survive review; bindings, normalized metadata,
        # and priority lists may have changed during the original save.
        return all(value.get(key) == original.get(key) for key in ('desc', 'note'))
    return value == item['value']


def _saved_metadata(audit, document, catalog, text):
    from tin.web import judgment_review as review

    if 'saved_items' in audit:
        return {tuple(item['path']): item for item in audit['saved_items']}
    candidate = audit.get('review_candidates')
    if not candidate:
        return {}
    statuses = {item['item_id']: item['status'] for item in audit.get('review_status', [])}
    decisions = {item['item_id']: item for item in audit.get('review', []) if item['action'] != 'dimension'}
    rule_positions = {rule['id']: (kind, index) for kind in quantity.RULE_KINDS
                      for index, rule in enumerate(document['judgment'][kind])}
    metadata, note_items, removed = {}, [], Counter()
    for item in review.review_items(candidate, candidate, catalog, text):
        path = tuple(item['path'])
        status = statuses.get(item['id'])
        decision = decisions.get(item['id'], {})
        saved = {'status': status, 'coverage_path': item['path'],
                 **{key: decision[key] for key in ('source_quote', 'caliber', 'number_confirmation',
                    'quantity_alignment') if key in decision}}
        last = path[-1]
        if status == 'excluded':
            if isinstance(last, int):
                removed[path[:-1]] += 1
            continue
        if item['kind'] in quantity.RULE_KINDS:
            path = rule_positions.get(item['value']['id'])
            if path is None:
                note_items.append((item, saved, _NOTE_REASONS['rule_kept' if status == 'kept_text' else 'rule_pending']))
                continue
        elif status == 'kept_text' and (item['kind'] == 'whitelist' or last in _DESCRIPTION_ENUMS):
            if isinstance(last, int):
                removed[path[:-1]] += 1
            note_items.append((item, saved, _NOTE_REASONS['whitelist' if item['kind'] == 'whitelist' else 'description']))
            continue
        elif isinstance(last, int):
            path = (*path[:-1], last - removed[path[:-1]])
        try:
            value = _item_value(document, path)
        except (KeyError, TypeError, IndexError):
            continue
        if _same_legacy_item(item, value, decision):
            metadata[path] = saved
    # Analysis builds retention notes after the original unstructured rows.
    # Only attach a legacy decision when the saved note matches exactly.
    index = len(candidate['unresolved']) - removed[('unresolved',)]
    for item, saved, reason in note_items:
        if index >= len(document['unresolved']):
            break
        if item['kind'] in quantity.RULE_KINDS:
            item['source_quote'] = saved.get('source_quote') or item['source_quote']
        if document['unresolved'][index] == review.note_for(item, reason):
            metadata[('unresolved', index)] = {
                'status': 'kept_text' if saved['status'] == 'kept_text' else 'pending',
                'coverage_path': item['path']}
        index += 1
    return metadata


def _rule_value(item, saved):
    """Review quantity from the source literal even after a previous conversion."""
    value = deepcopy(item['value'])
    if value.get('scale') is None:
        return value
    source = value.pop('source_value')
    scale = value.pop('scale')
    execution = {'source_value': source, 'scale': scale, 'value': value['value']}
    alignment = {
        'series_id': item['series_id'], 'source_quote': item['source_quote'],
        'alignment': {'mode': 'convert', 'exponent': Decimal(str(scale)).normalize().adjusted()},
    }
    confirmation = saved.get('quantity_alignment') or {}
    if all(confirmation.get(key) == value for key, value in alignment.items()):
        alignment = confirmation
    item['quantity_confirmation'] = {**alignment, 'execution': execution}
    if item['kind'] == 'thresholds':
        value['value'] = source
    else:
        match = EXPRESSION.fullmatch(value['expression'])
        item['quantity_confirmation']['execution']['expression'] = value['expression']
        value['expression'] = f'{match.group(1)} {match.group(2)} {source}'
        value.pop('value')
    return value


def _without_audit_times(value):
    return {key: field for key, field in (value or {}).items() if key not in ('confirmed_at', 'confirmed_by', 'catalog_version')}


def _binding_unit(item, known):
    sid = item['series_id']
    if sid is None:
        return None
    declaration = item.get('caliber_confirmation') or {}
    choice = item.get('caliber_choice') or {}
    if unit_present(declaration.get('unit')):
        return declaration['unit']
    if item['kind'] == 'falsifiers' and sid == item['ai_series_id']:
        return item['value']['unit_confirmed']
    if unit_present(choice.get('unit')):
        return choice['unit']
    if sid != item['ai_series_id']:
        return known.get(sid, {}).get('unit')
    return item.get('saved_binding_unit')


def _decision(item, known):
    caliber = item.get('caliber_choice')
    if caliber and caliber.get('mode') == 'catalog' and not caliber.get('scope_note') and not item.get('caliber_confirmation'):
        caliber = None
    alignment = item.get('quantity_confirmation') or {}
    value = item['value'] if isinstance(item['value'], dict) else {}
    return {'status': item['status'], 'series_id': item['series_id'], 'unit': _binding_unit(item, known),
            'related_series_ids': item['related_series_ids'], 'source_quote': item['source_quote'],
            'dimension': value.get('dimension'), 'note': value.get('note'),
            'caliber': _without_audit_times(caliber),
            'caliber_confirmation': _without_audit_times(item.get('caliber_confirmation')),
            'number_confirmation': _without_audit_times(item.get('number_confirmation')),
            'quantity_alignment': alignment.get('alignment'),
            'quantity_execution': alignment.get('execution')}


def initialize(preview, payload, catalog, saved_audit=None, *, dimensions=None):
    from tin.web import judgment_review as review

    audit = saved_audit or {}
    raw = payload.model_dump(mode='json', by_alias=True)
    text = payload.source_note or dump_markdown(payload, dimensions=dimensions)
    known = {field['id']: field for field in catalog}
    units = {field['id']: field.get('unit') for field in catalog}
    quotes = {key: quote for key, quote in (audit.get('rule_quotes') or {}).items() if isinstance(quote, str)}
    document = {'judgment': {key: value for key, value in raw.items() if key in ALLOWED_FIELDS},
                'unresolved': raw['unstructured'], 'rule_quotes': quotes,
                'binding_units': {**units, **(audit.get('binding_units') or {})}}
    items = review.review_items(document, document, catalog, text)
    saved_items = _saved_metadata(audit, document, catalog, text)
    for item in items:
        saved = saved_items.get(tuple(item['path']), {})
        value = item['series_id'] if item['kind'] == 'whitelist' else item['value']
        if 'item_key' in saved and saved['item_key'] != _item_key(value):
            saved = {}
        status = saved.get('status')
        if status not in _REVIEW_STATUSES:
            status = 'pending' if not review.has_binding(item) and (
                review.requires_binding(item) or item['kind'] == 'unresolved_binding') else 'confirmed'
            if item['kind'] == 'binding' and not review.has_binding(item) and status == 'confirmed':
                status = 'ready'
        if saved.get('source_quote') and saved['source_quote'] in text:
            item['source_quote'] = saved['source_quote']
        item['coverage_path'] = saved.get('coverage_path')
        if saved.get('caliber') and item['series_id']:
            item['caliber_choice'] = saved['caliber']
        if item['kind'] in quantity.RULE_KINDS:
            declaration = item['value'].get('caliber_confirmation')
            if declaration:
                field = known.get(item['series_id'], {})
                mode = 'catalog' if unit_present(field.get('unit')) else 'declared'
                item.update(caliber_confirmation=declaration, caliber_choice=saved.get('caliber') or {
                    'mode': mode, **declaration})
            number = saved.get('number_confirmation') or {}
            if (number.get('series_id') == item['series_id']
                    and number.get('quote') == item['source_quote']
                    and number.get('rule_id') == item['value']['id']):
                item['number_confirmation'] = number
            alignment = saved.get('quantity_alignment') or {}
            if (alignment.get('series_id') == item['series_id']
                    and alignment.get('source_quote') == item['source_quote']
                    and (alignment.get('alignment') or {}).get('mode') == 'keep'):
                item['quantity_confirmation'] = alignment
        item.update(status=status, initial_status=status, origin='saved', binding_explicit=review.has_binding(item),
                    initial_attention_reasons=[], initial_attention_fields=[],
                    saved_value=deepcopy(value))
        if item['kind'] in quantity.RULE_KINDS:
            item['value'] = _rule_value(item, saved)
        if status == 'pending':
            if not review.has_binding(item):
                item['initial_attention_fields'].append('binding')
            if item['kind'] == 'unresolved_binding':
                item['initial_attention_fields'].append('content')
        item['saved_binding_unit'] = saved.get('binding_unit', document['binding_units'].get(item['series_id']))
        item['saved_binding_unit'] = _binding_unit(item, known)
        item['saved_decision'] = _decision(item, known)
    preview.update(
        text=text, variety=payload.variety, written_at=raw['written_at'], base_payload=raw,
        source_coverage=audit.get('source_coverage') or {},
        review={'revision': 1, 'reuse_enabled': False, 'items': items, 'audit': [],
                'binding_warning_secret': secrets.token_hex(32), 'baseline': document,
                'candidate': document, 'catalog': deepcopy(catalog)},
    )
    preview['document'] = build_document(preview, catalog)


def _replacement(item, known):
    from tin.web.judgment_review import note_for

    state = _decision(item, known)
    saved = item['saved_decision']
    value = deepcopy(item['saved_value'])
    if state == saved:
        return value, None
    path, kind, status = item['path'], item['kind'], item['status']
    if status == 'excluded':
        return ('' if path[-1] in ('claim', 'text') else None), None
    if isinstance(value, dict):
        if state['dimension'] != saved['dimension']:
            value.pop('dimension', None)
            if state['dimension'] is not None:
                value['dimension'] = state['dimension']
        if state['note'] != saved['note']:
            value['note'] = state['note']
    if all(state[key] == saved[key] for key in state if key not in ('dimension', 'note')):
        return value, None
    if kind in quantity.RULE_KINDS:
        if status == 'kept_text':
            return None, note_for(item, _NOTE_REASONS['rule_kept'])
        value = quantity.confirmed_rule(item, deepcopy(item['value']))
        value.pop('caliber_confirmation', None)
        if item.get('caliber_confirmation'):
            value['caliber_confirmation'] = deepcopy(item['caliber_confirmation'])
        if kind == 'falsifiers':
            unit = (item.get('caliber_confirmation') or {}).get('unit') or known.get(item['series_id'], {}).get('unit')
            if unit_present(unit):
                value['unit_confirmed'] = unit
    if kind in {*quantity.RULE_KINDS, 'binding', 'unresolved_binding'}:
        # Remembered selector hints stay in the UI until explicitly confirmed.
        if status in {'pending', 'ready'} and item['remembered']:
            return value, None
        sid = item['series_id'] if status != 'kept_text' else None
        related = item['related_series_ids'] if status != 'kept_text' else []
        if 'series_id' in value or sid is not None:
            value['series_id'] = sid
        if related:
            value['related_series_ids'] = list(related)
        else:
            value.pop('related_series_ids', None)
        return value, None
    if status == 'kept_text' and kind == 'whitelist':
        return None, note_for(item, _NOTE_REASONS['whitelist'])
    if status == 'kept_text' and path[-1] in _DESCRIPTION_ENUMS:
        return None, note_for(item, _NOTE_REASONS['description'])
    if kind == 'whitelist' and not item['remembered']:
        return item['series_id'], None
    return value, None


def _project(preview, catalog):
    from tin.web import judgment_review as review

    baseline = preview['review']['baseline']
    document = deepcopy(baseline)
    known = {field['id']: field for field in catalog}
    locations, notes, removed = {}, [], Counter()
    for item in preview['review']['items']:
        path = tuple(item['path'])
        value, note = _replacement(item, known)
        root = document['unresolved'] if path[0] == 'unresolved' else document['judgment']
        review.set_path(root, path[1:] if path[0] == 'unresolved' else path, value)
        if value is None:
            if isinstance(path[-1], int):
                removed[path[:-1]] += 1
            if note is not None:
                notes.append((item['id'], note))
        else:
            locations[item['id']] = (*path[:-1], path[-1] - removed[path[:-1]]) if isinstance(path[-1], int) else path

    document = review.prune(document)
    for identifier, note in notes:
        locations[identifier] = ('unresolved', len(document['unresolved']))
        document['unresolved'].append(note)

    fields = document['judgment']
    priority = fields['priority_rule']
    if priority:
        identifiers = {rule['id'] for rule in fields['thresholds']}
        order = [identifier for identifier in priority['order'] if identifier in identifiers]
        if order != priority['order']:
            fields['priority_rule'] = {**priority, 'order': order} if order else None

    wanted = set(fields['whitelist'])
    changed_units, declared = {}, {}
    document['rule_quotes'] = {}
    for item in preview['review']['items']:
        location = locations.get(item['id'])
        if location is None:
            continue
        decision = _decision(item, known)
        saved = item['saved_decision']
        sid = decision['series_id']
        if sid and (sid != saved['series_id'] or decision['unit'] != saved['unit']):
            changed_units[sid] = decision['unit']
        root = document['unresolved'] if location[0] == 'unresolved' else fields
        value = review.at_path(root, location[1:] if location[0] == 'unresolved' else location)
        if not isinstance(value, dict):
            continue
        wanted.update(binding_ids(value))
        if location[0] in quantity.RULE_KINDS:
            if value.get('caliber_confirmation'):
                declared[value['series_id']] = value['caliber_confirmation']['unit']
            if item['source_quote']:
                document['rule_quotes'][value['id']] = item['source_quote']
    units = {**baseline['binding_units'], **changed_units, **declared}
    document['binding_units'] = {sid: units.get(sid, known.get(sid, {}).get('unit')) for sid in sorted(wanted)}
    return document, locations


def build_document(preview, catalog):
    return _project(preview, catalog)[0]


def _chart_references(preview):
    """Keep frozen chart content and keep evidence mounts with their original item."""
    items = preview['review']['items']
    evidence = {tuple(item['path'][1:]): item['status'] for item in items
                if len(item['path']) == 4 and item['path'][2] == 'evidence'}
    removed_rules = {item['saved_value']['id'] for item in items if item['kind'] in quantity.RULE_KINDS
                     and item['status'] in {'kept_text', 'excluded'}}
    references = []
    for reference in deepcopy(preview['base_payload']['chart_references']):
        target = reference.get('target', {'kind': 'judgment'})
        if target['kind'] in {'threshold', 'falsifier'} and target['id'] in removed_rules:
            reference['target'] = {'kind': 'judgment'}
        elif target['kind'] == 'evidence':
            side, index = target['side'], target['index']
            if evidence.get((side, 'evidence', index)) == 'excluded':
                reference['target'] = {'kind': 'judgment'}
            else:
                target['index'] -= sum(evidence.get((side, 'evidence', previous)) == 'excluded' for previous in range(index))
        references.append(reference)
    return references


def payload(preview, document, actor):
    raw = {**preview['base_payload'], **document['judgment'], 'unstructured': document['unresolved'],
           'author': actor, 'chart_references': _chart_references(preview)}
    try:
        result = JudgmentIn.model_validate(raw)
    except ValidationError as error:
        raise AnalysisError('修改后的研判条目格式无效，请检查条目和规则') from error
    if len(result.whitelist) != len(set(result.whitelist)):
        raise AnalysisError('指标白名单不能重复')
    return result


def confirmation_document(document):
    """Keep stored actuals in the saved payload, outside the model-draft validator."""
    fields = document['judgment']
    for side in ('bull', 'bear'):
        for item in fields['contradiction'][side]['evidence']:
            for key in ('value', 'as_of', 'source'):
                item.pop(key, None)
    cost = fields.get('cost_line')
    if cost is not None:
        cost.pop('value', None)
        cost.pop('source', None)
        for item in cost.get('items', []):
            item.pop('value', None)
    return document


def _item_value(document, path):
    from tin.web.judgment_review import at_path

    if path is None:
        return None
    if path[0] == 'unresolved':
        return at_path(document['unresolved'], path[1:])
    return at_path(document['judgment'], path)


def _value_details(value, decision):
    details = {'status': decision['status'],
               'series_id': decision['series_id'], 'related_series_ids': decision['related_series_ids']}
    if decision['series_id'] is not None:
        details['unit'] = decision['unit']
    if isinstance(value, dict):
        details.update({key: value[key] for key in ('value', 'expression', 'source_value', 'scale',
                        'unit_confirmed', 'caliber_confirmation', 'dimension', 'note') if key in value})
    return details


def changes(preview, document):
    catalog = preview['review']['catalog']
    known = {field['id']: field for field in catalog}
    _, locations = _project(preview, catalog)
    result = []
    for item in preview['review']['items']:
        decision = _decision(item, known)
        saved = item['saved_decision']
        if decision == saved or item['remembered'] and decision['status'] in {'pending', 'ready'}:
            continue
        value = _item_value(document, locations.get(item['id']))
        result.append({'item_id': item['id'], 'path': item['path'], 'title': item['title'],
                       'before': _value_details(item['saved_value'], saved),
                       'after': _value_details(value, decision)})
    return result


def saved_items(preview, document):
    """Save per-position decisions using the new payload's compacted indices."""
    catalog = preview['review']['catalog']
    known = {field['id']: field for field in catalog}
    _, locations = _project(preview, catalog)
    result = []
    for item in preview['review']['items']:
        path = locations.get(item['id'])
        value = _item_value(document, path)
        if value in (None, ''):
            continue
        result.append({'path': list(path), 'item_key': _item_key(value),
                       'status': item['status'], 'source_quote': item['source_quote'],
                       'binding_unit': _binding_unit(item, known),
                       'coverage_path': item['coverage_path'],
                       **{name: item[key] for name, key in (
                           ('caliber', 'caliber_choice'), ('number_confirmation', 'number_confirmation'),
                           ('quantity_alignment', 'quantity_confirmation')) if item.get(key)}})
    return result
