"""Model-output shape derived from judgment types, without server-owned data.

This schema describes syntax only. Catalog authorization, units, source quotes,
numbers and executable-rule checks remain the responsibility of analysis.py.
"""
from copy import deepcopy

from tin.schemas.judgment import EXPRESSION, JudgmentIn, PARADIGMS, TONES


SERVER_FIELDS = frozenset({
    'variety', 'author', 'written_at', 'review_period_days', 'source_note',
    'unstructured',
})


def _compact(node):
    """Remove presentation metadata and defaults; never fill model content."""
    if isinstance(node, dict):
        return {key: _compact(value) for key, value in node.items()
                if key not in {'title', 'default'}}
    if isinstance(node, list):
        return [_compact(value) for value in node]
    return node


def _bound(node):
    if isinstance(node, dict):
        if node.get('type') == 'string':
            node.setdefault('maxLength', 4000)
        elif node.get('type') == 'array':
            node.setdefault('maxItems', 32)
        for child in list(node.values()):
            _bound(child)
    elif isinstance(node, list):
        for child in node:
            _bound(child)


def model_output_schema():
    domain = _compact(JudgmentIn.model_json_schema(by_alias=True))
    definitions = domain.pop('$defs')
    unresolved = domain['properties']['unstructured']
    domain['properties'] = {name: field for name, field in domain['properties'].items()
                            if name not in SERVER_FIELDS}
    domain['required'] = [name for name in domain['required'] if name not in SERVER_FIELDS]

    # These enums are validated by domain field validators, so Pydantic's schema
    # does not include them automatically. Use the same authoritative constants.
    for name, choices in (('paradigm', PARADIGMS), ('tone', TONES)):
        domain['properties'][name] = {
            'anyOf': [{'type': 'string', 'enum': list(choices)}, {'type': 'null'}]}

    # The stored domain also holds verified facts. A model draft cannot supply
    # those facts, even if their JSON types would otherwise be valid.
    for name, fields in (
            ('Evidence', ('value', 'as_of', 'source')),
            ('CostLine', ('value', 'source')),
            ('CostItem', ('value',))):
        for field in fields:
            definitions[name]['properties'][field] = {'type': 'null'}

    pending = definitions['UnstructuredItem']
    pending['required'] = [name for name in pending['properties'] if name != 'series_id']
    rule_kind = next(deepcopy(item) for item in pending['properties']['rule_kind']['anyOf']
                     if item.get('type') != 'null')
    pending['anyOf'] = [
        {'properties': {'business_condition': {'type': 'null'},
                        'rule_kind': {'type': 'null'}}},
        {'properties': {'business_condition': {'type': 'string', 'minLength': 1},
                        'rule_kind': rule_kind}},
    ]

    threshold = definitions['Threshold']
    pair = {'type': 'array', 'items': {'type': 'number'}, 'minItems': 2, 'maxItems': 2}
    moving = {'$ref': '#/$defs/MovingAverageTarget'}
    threshold['properties']['value'] = {'oneOf': [{'type': 'number'}, deepcopy(pair), deepcopy(moving)]}
    threshold.update({
        'if': {'properties': {'operator': {'const': 'between'}}, 'required': ['operator']},
        'then': {'properties': {'value': deepcopy(pair)}},
        'else': {'properties': {'value': {'oneOf': [{'type': 'number'}, deepcopy(moving)]}}},
    })
    threshold['allOf'] = [{
        'if': {'properties': {'operator': {'enum': ['crosses_below', 'crosses_above']}}, 'required': ['operator']},
        'then': {'properties': {'value': deepcopy(moving)}},
    }]
    for name in ('Threshold', 'Falsifier'):
        definitions[name]['properties']['id']['pattern'] = r'^[A-Za-z][A-Za-z0-9_-]{0,63}$'
        definitions[name]['properties']['series_id']['minLength'] = 1
    falsifier = definitions['Falsifier']['properties']
    falsifier['expression']['pattern'] = EXPRESSION.pattern
    falsifier['consecutive_periods']['maximum'] = 120
    for name in ('Evidence', 'MarginalItem', 'CostItem', 'UnstructuredItem'):
        definitions[name]['properties']['series_id']['anyOf'][0]['minLength'] = 1
    domain['properties']['whitelist']['uniqueItems'] = True
    definitions['PriorityRule']['properties']['order']['uniqueItems'] = True

    result = {
        '$schema': 'https://json-schema.org/draft/2020-12/schema',
        '$defs': definitions,
        'type': 'object',
        'additionalProperties': False,
        'required': ['judgment', 'binding_units', 'rule_quotes', 'unresolved'],
        'properties': {
            'judgment': domain,
            'binding_units': {'type': 'object', 'additionalProperties': {
                'anyOf': [{'type': 'string'}, {'type': 'null'}]}},
            'rule_quotes': {'type': 'object', 'additionalProperties': {
                'type': 'string', 'minLength': 1}},
            'unresolved': unresolved,
        },
    }
    _bound(result)
    for name in ('thresholds', 'falsifiers'):
        domain['properties'][name]['maxItems'] = 24
    return result


OUTPUT_SCHEMA = model_output_schema()
