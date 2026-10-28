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
