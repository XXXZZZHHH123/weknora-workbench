"""Names and ordered groups for explicit saved dimension tags; no classification."""
from tin.web.research_dimensions import default_dimensions


def dimension_name(identifier, dimensions):
    if not identifier:
        return '未标注'
    return next((item['label'] for item in dimensions if item['id'] == identifier), '已删除维度')


def evidence_groups(sides, dimensions=None):
    dimensions = default_dimensions() if dimensions is None else dimensions
    groups = {item['id']: {'id': item['id'], 'label': item['label'], 'bull': [], 'bear': []}
              for item in dimensions}
    for side in ('bull', 'bear'):
        for index, evidence in enumerate(sides.get(side, {}).get('evidence', [])):
            identifier = evidence.get('dimension')
            group = groups.setdefault(identifier, {'id': identifier,
                'label': dimension_name(identifier, dimensions), 'bull': [], 'bear': []})
            # Chart mounts refer to the saved evidence index, including after grouping.
            group[side].append({**evidence, 'evidence_index': index})
    return [group for key, group in groups.items() if key is not None and (group['bull'] or group['bear'])] + [
        group for key, group in groups.items() if key is None and (group['bull'] or group['bear'])]
