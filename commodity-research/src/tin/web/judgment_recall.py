"""Large-catalog selection: per research item, model phrasings -> ranked recall -> model picks.

The whole catalog never goes to the model at once, so the model's output stays
bounded by the items it was shown. Recall widens what the model can see; the model
decides; the program accepts only references it actually showed in this request.
"""
import json
from datetime import datetime

from fastapi import HTTPException
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI
from tin.integration.catalog_recall import CatalogRecall
from tin.judgments.prompt import (RECALL_BATCH_RECOMMEND_PROTOCOL, RECALL_PICK_PROTOCOL,
    RECALL_QUERY_PROTOCOL, RECALL_RECOMMEND_PROTOCOL)

QUERIES_PER_ITEM = 5
QUERY_CHARS = 60
QUOTE_QUERY_CHARS = 200
OVERVIEW_BYTES = 32 * 1024
OVERVIEW_FIELDS_PER_SHEET = 5
OVERVIEW_FIELD_CHARS = 40
SHARED_CANDIDATE_LIMIT = 40
RECOMMENDATION_LIMIT = 5
RECOMMENDATION_REASON_CHARS = 40
# Keep the ranked response as well as the prompt inside the model budget.
RECOMMENDATION_BATCH_ITEMS = 16
# Candidates per item, tried from the most generous until the request fits.
RECALL_STEPS = (20, 12, 8)
DISPLAY_KEYS = ('name', 'sheet', 'unit', 'frequency', 'dimensions', 'source_file', 'source_kind')
SELECTION_INVALID = '模型未给出有效的真实指标选择'
RECOMMENDATION_INVALID = '模型未给出有效的真实指标推荐'


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


def catalog_fields(catalog):
    return [{**field, 'id': identifier, 'name': field.get('metric') or field.get('name') or identifier}
            for identifier, field in catalog.items()]


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


def recall_groups(index, inventory, phrased, per_item, *, shared=(), search='', all_items=False):
    # Without any usable phrasing every item is still recalled by its own verbatim quote.
    items = [item for item in inventory['items'] if all_items or not phrased or shared or item['id'] in phrased]
    groups = []
    for item in items:
        found = index.recall([*phrased.get(item['id'], []), search, item['quote'][:QUOTE_QUERY_CHARS]], per_item)
        if found or shared or all_items:
            groups.append((item, found))
    return groups


def pick_request(text, groups, catalog_by_id, *, shared=(), recommend=False, batch=False):
    ordered = list(dict.fromkeys([*(identifier for _, found in groups for identifier in found), *shared]))
    refs = {identifier: f'r{position + 1}' for position, identifier in enumerate(ordered)}
    candidates = {refs[identifier]: {key: catalog_by_id[identifier][key] for key in DISPLAY_KEYS
                                     if catalog_by_id[identifier].get(key) not in (None, '', {}, [])}
                  for identifier in ordered}
    items = [{'id': item['id'], 'kind': item['kind'], 'quote': item['quote'],
              'candidates': [refs[identifier] for identifier in found]} for item, found in groups]
    protocol = RECALL_BATCH_RECOMMEND_PROTOCOL if batch else RECALL_RECOMMEND_PROTOCOL if recommend else RECALL_PICK_PROTOCOL
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


def ranked_references(candidates, mapping, allowed):
    if not isinstance(candidates, list) or len(candidates) > RECOMMENDATION_LIMIT:
        raise HTTPException(422, RECOMMENDATION_INVALID)
    ranked, seen = [], set()
    for candidate in candidates:
        if not isinstance(candidate, dict) or set(candidate) != {'ref', 'reason'}:
            raise HTTPException(422, RECOMMENDATION_INVALID)
        reference, reason = candidate['ref'], candidate['reason']
        if not isinstance(reference, str) or not isinstance(reason, str) or not 1 <= len(reason.strip()) <= RECOMMENDATION_REASON_CHARS:
            raise HTTPException(422, RECOMMENDATION_INVALID)
        identifier = mapping.get(reference)
        ranked.append((identifier, reason.strip()) if identifier in allowed and identifier not in seen else None)
        seen.add(identifier)
    return ranked


def picked_recommendations(result, groups, mapping, shared, *, batch):
    if not isinstance(result, dict) or set(result) != {'recommendations'}:
        raise HTTPException(422, RECOMMENDATION_INVALID)
    value = result['recommendations']
    if not batch:
        item, found = groups[0]
        return {item['id']: (False, ranked_references(value, mapping, set(found) | set(shared)))}
    if not isinstance(value, dict) or value.keys() - {item['id'] for item, _ in groups}:
        raise HTTPException(422, RECOMMENDATION_INVALID)
    ranked = {}
    for item, found in groups:
        answer = value.get(item['id'], {'ambiguous': False, 'candidates': []})
        if (not isinstance(answer, dict) or set(answer) != {'ambiguous', 'candidates'}
                or type(answer['ambiguous']) is not bool):
            raise HTTPException(422, RECOMMENDATION_INVALID)
        ranked[item['id']] = (answer['ambiguous'], ranked_references(answer['candidates'], mapping, set(found) | set(shared)))
    return ranked


def observed_ids(scope, catalog, identifiers, module):
    """Check only the model's selected, authorized IDs with a one-point window."""
    day = datetime.now(SHANGHAI).date()
    if not identifiers:
        return set()
    if scope.scope_version != 2:
        if module == 'diagram':
            from tin.web.diagram_generation import candidate_status
            return set(candidate_status(scope, identifiers))
        from tin.web.judgment_radar import legacy_series
        return {sid for sid, field in legacy_series(scope, identifiers, day, list(catalog.values()), 1).items()
                if field['points']}

    from tin.integration.page_facts import bounded_facts, catalog_read_scope

    observed, versions = set(), {}
    with catalog_read_scope():
        for offset in range(0, len(identifiers), 200):
            selection = identifiers[offset:offset + 200]
            facts = bounded_facts(scope, selection, module, day=day.isoformat(), points=1, direct_only=True)
            for binding, version in facts.get('source_versions', {}).items():
                if binding in versions and version != versions[binding]:
                    raise HTTPException(409, '推荐指标的观测来源已改变，请重新推荐')
                versions[binding] = version
            for sid in selection:
                field = catalog[sid]
                binding, version = field.get('source_binding_id'), field.get('source_version')
                if binding and version is not None and versions.get(binding) != version:
                    raise HTTPException(409, '指标目录与观测来源不一致，请重新推荐')
            observed.update(row['series_id'] for row in facts['rows'])
    return observed


async def recommend(scope, model_id, catalog, text, inventory, *, search='', progress,
                    compatible=None, module='judgments', shared=()):
    """Run one bounded recommendation job, issuing its model calls serially."""
    from tin.web import judgment_generation as generation

    completion = generation.selection_completion
    if module == 'diagram':
        from tin.web.diagram_generation import completion
    items = inventory['items']
    if not items:
        return {}
    by_id = {field['id']: field for field in catalog}
    used = await run_in_threadpool(shared_candidate_ids, scope, catalog)
    shared = tuple(dict.fromkeys([*(sid for sid in shared if sid in by_id), *used]))[:SHARED_CANDIDATE_LIMIT]
    index = await run_in_threadpool(catalog_index, catalog)
    batch = module == 'diagram' or len(items) > 1
    ranked = {}
    for offset in range(0, len(items), RECOMMENDATION_BATCH_ITEMS):
        portion = {'items': items[offset:offset + RECOMMENDATION_BATCH_ITEMS]}
        if not catalog:
            break
        await progress('selecting')
        query = query_prompt(text, portion, catalog, prompt_bytes=generation.PROMPT_BYTES, search=search)
        phrased = item_queries(await completion(scope, model_id, query), portion)
        largest = recall_groups(index, portion, phrased, RECALL_STEPS[0], shared=shared, search=search, all_items=True)
        for per_item in RECALL_STEPS:
            groups = [(item, found[:per_item]) for item, found in largest if found or shared]
            prompt, mapping = pick_request(text, groups, by_id, shared=shared, recommend=True, batch=batch)
            if len(prompt.encode('utf-8')) <= generation.PROMPT_BYTES:
                break
        else:
            raise HTTPException(413, '完整资料与召回候选超过本次容量，请缩小范围后重试')
        if mapping:
            await progress('analyzing')
            result = await completion(scope, model_id, prompt)
            ranked.update(picked_recommendations(result, groups, mapping, shared, batch=batch))

    await progress('validating')
    identifiers = list(dict.fromkeys(pair[0] for _, candidates in ranked.values() for pair in candidates if pair))
    observed = await run_in_threadpool(observed_ids, scope, by_id, identifiers, module)
    reviewed = {}
    for item in items:
        ambiguous, candidates = ranked.get(item['id'], (False, []))
        checked = []
        for pair in candidates:
            candidate = None
            if pair and pair[0] in observed:
                field, reason = by_id[pair[0]], pair[1]
                if compatible is None or compatible(item, field):
                    candidate = {'series_id': field['id'], 'reason': reason,
                                 **{key: field.get(key) for key in DISPLAY_KEYS}}
            checked.append(candidate)
        reviewed[item['id']] = {'recommended': checked[0] if checked and not ambiguous else None,
                                'recommendations': [candidate for candidate in checked if candidate],
                                'ambiguous': ambiguous}
    return reviewed
