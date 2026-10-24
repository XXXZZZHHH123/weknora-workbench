"""Presentation-only market names; bindings and original evidence stay intact."""
from dataclasses import replace


def _text(value):
    return value.strip() if isinstance(value, str) and value.strip() else None


def _metadata_text(value):
    if isinstance(value, dict):
        return '；'.join(f'{key}：{_metadata_text(item)}' for key, item in value.items() if item is not None)
    if isinstance(value, list):
        return '、'.join(_metadata_text(item) or '' for item in value)
    if type(value) in {int, float, bool}:
        return str(value)
    return _text(value)


def compact_market_card(card, metadata=None):
    metadata = metadata if isinstance(metadata, dict) else {}
    original = _text(metadata.get('metric')) or card.label
    explicit = _text(metadata.get('display_name')) or _text(metadata.get('name'))
    title = explicit or original
    if not explicit:
        # Scanner headers can combine a name with separately verified metadata.
        # A slash alone is not proof: ratio/comparison names keep both sides.
        parts = original.split(' / ')
        decorations = {_metadata_text(metadata.get(key)) for key in
                       ('unit', 'frequency', 'code', 'vendor_code', 'coverage')}
        decorations.discard(None)
        if len(parts) > 1 and parts[1].strip() in decorations:
            title = parts[0].strip() or original
    entries = []
    for label, value in [('来源', card.source or card.badge),
                         ('代码', _text(metadata.get('code')) or _text(metadata.get('vendor_code'))),
                         ('频率', metadata.get('frequency')), ('覆盖说明', metadata.get('coverage')),
                         ('口径说明', card.caliber)]:
        value = _metadata_text(value)
        if value:
            entries.append({'label': label, 'value': value})
    detail = {**card.detail, 'display': {'title': title, 'full_name': original, 'metadata': entries}}
    return replace(card, label=title, detail=detail)


def compact_market_cards(session, cards):
    catalog = session.info.get('knowledge_facts_catalog')
    if not isinstance(catalog, dict):
        return cards
    return [compact_market_card(card, catalog.get(card.series_id)) for card in cards]
