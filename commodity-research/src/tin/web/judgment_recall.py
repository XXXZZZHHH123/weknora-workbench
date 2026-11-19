"""Large-catalog selection: per research item, model phrasings -> ranked recall -> model picks.

The whole catalog never goes to the model at once, so the model's output stays
bounded by the items it was shown. Recall widens what the model can see; the model
decides; the program accepts only references it actually showed in this request.
"""
import json

from fastapi import HTTPException

from tin.integration.catalog_recall import CatalogRecall
from tin.judgments.prompt import RECALL_PICK_PROTOCOL, RECALL_QUERY_PROTOCOL, RECALL_RECOMMEND_PROTOCOL

QUERIES_PER_ITEM = 5
QUERY_CHARS = 60
QUOTE_QUERY_CHARS = 200
OVERVIEW_BYTES = 32 * 1024
OVERVIEW_FIELDS_PER_SHEET = 5
OVERVIEW_FIELD_CHARS = 40
SHARED_CANDIDATE_LIMIT = 40
RECOMMENDATION_LIMIT = 5
RECOMMENDATION_REASON_CHARS = 40
# Candidates per item, tried from the most generous until the request fits.
RECALL_STEPS = (20, 12, 8)
DISPLAY_KEYS = ('name', 'sheet', 'unit', 'frequency', 'dimensions', 'source_file', 'source_kind')
SELECTION_INVALID = '模型未给出有效的真实指标选择'


def _encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':'))


def _items(inventory):
    return [{'id': item['id'], 'kind': item['kind'], 'quote': item['quote']} for item in inventory['items']]


def catalog_overview(catalog, budget):
    """Keep catalog order, using the same sample count for every worksheet."""
    sheets = {}
    for row in catalog:
        sheet = row.get('sheet') or row.get('source_file') or ''
        names = sheets.setdefault((row.get('knowledge_base_id'), row.get('source_file'), sheet), {})
        name = row.get('name')
        if isinstance(name, str) and name and len(names) < OVERVIEW_FIELDS_PER_SHEET:
            # Some directories record each field's qualifiers separately from its metric.
            dims = row.get('dimensions')
            if isinstance(dims, dict):
                qualifiers = [str(value) for value in dims.values() if type(value) in (str, int, float) and value != '']
                name = ' / '.join([name, *qualifiers])
            names.setdefault(name, name[:OVERVIEW_FIELD_CHARS])
    for width in range(OVERVIEW_FIELDS_PER_SHEET, -1, -1):
        overview = _encode([[sheet, list(names.values())[:width]] for (_, _, sheet), names in sheets.items()])
        if len(overview.encode('utf-8')) <= budget:
            return overview
    raise HTTPException(413, '指标目录的表名超过本次检索容量，请缩小来源范围后重试')


def query_prompt(text, inventory, catalog=None, *, prompt_bytes=None, search=''):
    end = ('\n研究条目：' + _encode(_items(inventory))
           + ('\n研究员检索提示：' + _encode(search) if search else '') + '\n完整原文：\n' + text)
    if catalog is None:
        return RECALL_QUERY_PROTOCOL + end
    start = RECALL_QUERY_PROTOCOL + '\n目录用语概览（表名与字段名样例）：'
    budget = OVERVIEW_BYTES
    if prompt_bytes is not None:
        budget = min(budget, prompt_bytes - len((start + end).encode('utf-8')))
    if budget < 2:
        raise HTTPException(413, '完整资料超过本次指标检索容量，请缩小范围后重试')
    return start + catalog_overview(catalog, budget) + end


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


def shared_candidate_ids(scope, catalog):
    """Only references in this workbench and its current directory are eligible."""
    if not catalog or scope is None or scope.scope_version != 2:
        return []
    from tin.integration.workbench_scope import artifact_session
    from tin.models import SourceMapping, WorkbenchArtifact
    from tin.web import charts, monitors
    from tin.web.chart_data import definition_series_ids
    from tin.web.dimension_board import _items as dimension_bindings
    from tin.web.dimension_data import pinned_definition
    from tin.web.research_dimensions import DIMENSIONS_KEY, read_dimensions
    from tin.web.restricted_views import CORE_KEY, core_items, mapping_key

    allowed = {row['id'] for row in catalog}
    uses = []
    with artifact_session(scope) as session:
        def record_chart(row, used_at):
            try:
                definition = charts.decode_definition(row)
            except HTTPException as error:
                if error.status_code != 409:
                    raise
                return  # An invalid chart has no verified curve bindings.
            uses.append((used_at, definition_series_ids(definition)))

        if 'indicators' in scope.view_modules:
            for row in session.scalars(charts.artifact_query().order_by(WorkbenchArtifact.id)):
                record_chart(row, row.updated_at)
            row = session.get(SourceMapping, mapping_key(session, DIMENSIONS_KEY))
            if row is not None:
                for binding in dimension_bindings(read_dimensions(session)):
                    if binding['kind'] == 'indicator':
                        uses.append((row.confirmed_at, [binding['id']]))
                    else:
                        try:
                            definition = pinned_definition(session, binding)
                        except HTTPException as error:
                            if error.status_code not in (404, 409):
                                raise
                        else:
                            uses.append((row.confirmed_at, definition_series_ids(definition)))

        if 'market' in scope.view_modules:
            row = session.get(SourceMapping, mapping_key(session, CORE_KEY))
            if row is not None:
                for binding in core_items(session):
                    if binding['kind'] == 'indicator':
                        uses.append((row.confirmed_at, [binding['id']]))
                    else:
                        chart = session.scalar(charts.artifact_query().where(WorkbenchArtifact.id == binding['id']))
                        if chart is not None:
                            record_chart(chart, row.confirmed_at)

        if 'home' in scope.view_modules:
            for row in monitors._rows(session, scope):
                monitor = monitors._body(row)
                if monitor['enabled'] is True:
                    uses.append((row.updated_at, [rule['series_id'] for rule in monitor['rules']]))

    uses.sort(key=lambda use: use[0], reverse=True)
    return list(dict.fromkeys(sid for _, identifiers in uses for sid in identifiers if sid in allowed))[:SHARED_CANDIDATE_LIMIT]


def recall_groups(index, inventory, phrased, per_item, *, shared=(), search=''):
    # Without any usable phrasing every item is still recalled by its own verbatim quote.
    items = [item for item in inventory['items'] if not phrased or shared or item['id'] in phrased]
    groups = []
    for item in items:
        found = index.recall([*phrased.get(item['id'], []), search, item['quote'][:QUOTE_QUERY_CHARS]], per_item)
        if found or shared:
            groups.append((item, found))
    return groups


def pick_request(text, groups, catalog_by_id, *, shared=(), recommend=False):
    ordered = list(dict.fromkeys([*(identifier for _, found in groups for identifier in found), *shared]))
    refs = {identifier: f'r{position + 1}' for position, identifier in enumerate(ordered)}
    candidates = {refs[identifier]: {key: catalog_by_id[identifier][key] for key in DISPLAY_KEYS
                                     if catalog_by_id[identifier].get(key) not in (None, '', {}, [])}
                  for identifier in ordered}
    items = [{'id': item['id'], 'kind': item['kind'], 'quote': item['quote'],
              'candidates': [refs[identifier] for identifier in found]} for item, found in groups]
    protocol = RECALL_RECOMMEND_PROTOCOL if recommend else RECALL_PICK_PROTOCOL
    prompt = (protocol + ('\n本研究台已使用的指标（共享候选）：' + _encode([refs[sid] for sid in shared]) if shared else '')
              + '\n研究条目：' + _encode(items) + '\n候选指标：' + _encode(candidates)
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


def picked_recommendations(result, mapping):
    invalid = '模型未给出有效的真实指标推荐'
    if not isinstance(result, dict) or set(result) != {'recommendations'}:
        raise HTTPException(422, invalid)
    recommendations = result['recommendations']
    if not isinstance(recommendations, list) or len(recommendations) > RECOMMENDATION_LIMIT:
        raise HTTPException(422, invalid)
    chosen = {}
    for item in recommendations:
        if not isinstance(item, dict) or set(item) != {'ref', 'reason'}:
            raise HTTPException(422, invalid)
        reference, reason = item['ref'], item['reason']
        if (not isinstance(reference, str) or reference not in mapping or reference in chosen
                or not isinstance(reason, str) or not 1 <= len(reason.strip()) <= RECOMMENDATION_REASON_CHARS):
            raise HTTPException(422, invalid)
        chosen[reference] = reason.strip()
    return [(mapping[reference], reason) for reference, reason in chosen.items()]
