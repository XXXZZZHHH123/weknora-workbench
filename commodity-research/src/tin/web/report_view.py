"""A readable projection of report records; calculations stay in the snapshot."""


def present_snapshot(payload, names=None):
    names = names or {}
    observations = []
    for item in payload.get('observations', []):
        row = dict(item)
        row['name'] = item.get('name') or names.get(item['series_id']) or item['series_id']
        observations.append(row)
    sections = {key: payload.get(key) or [] for key in ('derived', 'signals', 'gaps')}
    return {**sections, 'observations': observations, 'judgment': payload.get('judgment'),
            'counts': {'observations': len(observations),
                       **{key: len(rows) for key, rows in sections.items()}}}
