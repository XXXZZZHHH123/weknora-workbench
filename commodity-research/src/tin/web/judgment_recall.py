"""Large-catalog selection: per research item, model phrasings -> ranked recall -> model picks.

The whole catalog never goes to the model at once, so the model's output stays
bounded by the items it was shown. Recall widens what the model can see; the model
decides; the program accepts only references it actually showed in this request.
"""
import json

from fastapi import HTTPException

from tin.integration.catalog_recall import CatalogRecall
from tin.judgments.prompt import RECALL_PICK_PROTOCOL, RECALL_QUERY_PROTOCOL

QUERIES_PER_ITEM = 3
QUERY_CHARS = 60
QUOTE_QUERY_CHARS = 200
# Candidates per item, tried from the most generous until the request fits.
RECALL_STEPS = (20, 12, 8)
DISPLAY_KEYS = ('name', 'sheet', 'unit', 'frequency', 'dimensions', 'source_file', 'source_kind')
SELECTION_INVALID = '模型未给出有效的真实指标选择'


def _encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':'))


def _items(inventory):
    return [{'id': item['id'], 'kind': item['kind'], 'quote': item['quote']} for item in inventory['items']]


def query_prompt(text, inventory):
    return RECALL_QUERY_PROTOCOL + '\n研究条目：' + _encode(_items(inventory)) + '\n完整原文：\n' + text


def item_queries(result, inventory):
    """Phrasings only steer recall, so malformed parts are dropped rather than failing the analysis."""
    known = {item['id'] for item in inventory['items']}
    value = result.get('queries') if isinstance(result, dict) and 'queries' in result else result
    phrased = {}
    if not isinstance(value, dict):
        return phrased
    for identifier, queries in value.items():
        if identifier not in known:
            continue
        if isinstance(queries, str):
            queries = [queries]
        if not isinstance(queries, list):
            continue
        clean = [query.strip() for query in queries
                 if isinstance(query, str) and query.strip() and len(query.strip()) <= QUERY_CHARS]
        if clean:
            phrased[identifier] = list(dict.fromkeys(clean))[:QUERIES_PER_ITEM]
    return phrased


def catalog_index(catalog):
    return CatalogRecall((row['id'], [row.get('name'), row.get('dimensions')],
                          [row.get(key) for key in ('sheet', 'unit', 'frequency', 'source_file')])
                         for row in catalog)


def recall_groups(index, inventory, phrased, per_item):
    # Without any usable phrasing every item is still recalled by its own verbatim quote.
    items = [item for item in inventory['items'] if item['id'] in phrased] if phrased else list(inventory['items'])
    groups = []
    for item in items:
        found = index.recall([*phrased.get(item['id'], []), item['quote'][:QUOTE_QUERY_CHARS]], per_item)
        if found:
            groups.append((item, found))
    return groups


def pick_request(text, groups, catalog_by_id):
    ordered = list(dict.fromkeys(identifier for _, found in groups for identifier in found))
    refs = {identifier: f'r{position + 1}' for position, identifier in enumerate(ordered)}
    candidates = {refs[identifier]: {key: catalog_by_id[identifier][key] for key in DISPLAY_KEYS
                                     if catalog_by_id[identifier].get(key) not in (None, '', {}, [])}
                  for identifier in ordered}
    items = [{'id': item['id'], 'kind': item['kind'], 'quote': item['quote'],
              'candidates': [refs[identifier] for identifier in found]} for item, found in groups]
    prompt = (RECALL_PICK_PROTOCOL + '\n研究条目：' + _encode(items) + '\n候选指标：' + _encode(candidates)
              + '\n完整原文：\n' + text)
    return prompt, {reference: identifier for identifier, reference in refs.items()}


def picked_ids(result, mapping):
    """Accept only references shown in this request; never guess or repair an ID."""
    value = result.get('picks') if isinstance(result, dict) and 'picks' in result else result
    if not isinstance(value, dict):
        raise HTTPException(422, SELECTION_INVALID)
    chosen = []
    for references in value.values():
        if references is None:
            continue
        if isinstance(references, str):
            references = [references]
        if (not isinstance(references, list)
                or any(not isinstance(reference, str) or reference not in mapping for reference in references)):
            raise HTTPException(422, SELECTION_INVALID)
        chosen.extend(mapping[reference] for reference in references)
    return list(dict.fromkeys(chosen))
