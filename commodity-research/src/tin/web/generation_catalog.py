"""Lossless directory transport; dictionary references never identify an indicator."""
import json


def column_catalog(catalog):
    if (not catalog or any(not isinstance(field, dict) or 'id' not in field or 'name' not in field
                           or 'source_ref' in field for field in catalog)):
        return None
    columns = ['id', 'name', *sorted({key for field in catalog for key in field} - {'id', 'name'})]
    dictionaries = {key: [] for key in columns[2:]}
    positions = {key: {} for key in columns[2:]}
    rows = []
    for field in catalog:
        # Actual IDs and names stay visible, verbatim, in every directory row.
        row = [field['id'], field['name']]
        for key in columns[2:]:
            if key not in field:
                row.append(-1)
                continue
            value = field[key]
            identity = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'), allow_nan=False)
            if identity not in positions[key]:
                positions[key][identity] = len(dictionaries[key])
                dictionaries[key].append(value)
            row.append(positions[key][identity])
        rows.append(row)
    return json.dumps({'format': 'research-catalog-columns.v1', 'columns': columns,
                       'dictionaries': dictionaries, 'indicators': rows},
                      ensure_ascii=False, separators=(',', ':'), allow_nan=False)


def selection_catalog(catalog, refs, *, kind=None):
    """Encode temporary selector references without modifying source fields."""
    if kind not in (None, 'flat', 'columns'):
        raise ValueError('unsupported selector directory encoding')
    if (not isinstance(catalog, (list, tuple)) or not isinstance(refs, (list, tuple))
            or len(catalog) != len(refs)
            or any(not isinstance(reference, str) or not reference for reference in refs)):
        raise ValueError('selector references must match the directory rows')
    if len(set(refs)) != len(refs):
        raise ValueError('selector references must be unique')
    if any(not isinstance(field, dict) or 'id' not in field or 'name' not in field
           or any(not isinstance(key, str) for key in field) for field in catalog):
        raise ValueError('selector directory rows require id, name and string property names')

    def encode(document):
        return json.dumps(document, ensure_ascii=False, separators=(',', ':'), allow_nan=False)

    flat = None
    if kind != 'columns':
        flat = encode({'format': 'research-selection-flat.v1',
                       'indicators': dict(zip(refs, catalog))})
        if kind == 'flat':
            return flat, 'flat'

    columns = ['id', 'name', *sorted({key for field in catalog for key in field} - {'id', 'name'})]
    dictionaries = {key: [] for key in columns[2:]}
    positions = {key: {} for key in columns[2:]}
    rows = {}
    for reference, field in zip(refs, catalog):
        row = [field['id'], field['name']]
        for key in columns[2:]:
            if key not in field:
                row.append(-1)
                continue
            value = field[key]
            identity = json.dumps(value, ensure_ascii=False, sort_keys=True,
                                  separators=(',', ':'), allow_nan=False)
            if identity not in positions[key]:
                positions[key][identity] = len(dictionaries[key])
                dictionaries[key].append(value)
            row.append(positions[key][identity])
        rows[reference] = row
    compact = encode({'format': 'research-selection-columns.v1', 'columns': columns,
                      'dictionaries': dictionaries, 'indicators': rows})
    if kind == 'columns' or len(compact.encode('utf-8')) < len(flat.encode('utf-8')):
        return compact, 'columns'
    return flat, 'flat'
