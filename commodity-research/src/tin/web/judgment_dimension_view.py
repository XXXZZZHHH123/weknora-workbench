"""Presentation helpers for explicit saved research bindings."""

from collections import defaultdict

from tin.web.report_watch import watch_items


def dimension_name(identifier, dimensions):
    if not identifier:
        return '未标注'
    return next((item['label'] for item in dimensions if item['id'] == identifier), '已删除维度')


def pending_note_entries(payload):
    """Yield displayable saved notes and their unique, explicitly bound rule."""
    rules = defaultdict(list)
    for kind, items in (('threshold', payload.thresholds), ('falsifier', payload.falsifiers)):
        for rule in items:
            rules[kind, rule.series_id].append(rule.id)
    for index, note in enumerate(payload.unstructured):
        candidates = rules.get((note.rule_kind, note.series_id), []) if note.series_id and note.rule_kind else []
        target = (note.rule_kind, candidates[0]) if len(candidates) == 1 else None
        condition = (note.business_condition or '').strip()
        if not condition and target is None:
            # Older saved conditions can lack the structured condition fields.
            legacy = watch_items({'unstructured': [note.model_dump(mode='json')]})
            if not legacy:
                continue
            condition = legacy[0]['text']
        yield index, note, condition, target
