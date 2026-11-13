"""Additive, source-bound result metadata. This is not SQL cell lineage.

The digest uses a typed, length-framed encoding (see Go internal/structuredresult)
instead of language-specific JSON float/Unicode formatting. Transport scope is
deliberately outside this digest and remains subject to the bridge's ACL checks.
"""
from __future__ import annotations

from copy import deepcopy
from decimal import Decimal
import hashlib
import math


HASH_ALGORITHM = 'sha256-typed-json-v1'
# A fixed, versioned projection: server/Go may add transport kind/scope, but may
# not change these actual values, input versions, coordinates or limitations.
HASH_INPUT_KEYS = frozenset({
    'columns', 'rows', 'sql', 'source', 'table_id', 'dataset_id', 'series',
    'filters', 'requested_range', 'coverage', 'provenance', 'row_provenance',
    'record_sha256', 'bundle_sha256', 'plan_sha256', 'draft_id',
    'inputs', 'input_count', 'input_complete', 'output_complete',
    'returned_row_count', 'output_truncated', 'truncated', 'offset',
    'total_row_count', 'record_count', 'scope_record_count', 'valid_count',
    'missing_count', 'aggregate', 'extremum_rows', 'extremum_count', 'rows_are',
    'actual_first_date', 'actual_last_date', 'valid_first_date', 'valid_last_date',
    'observation_coverage', 'source_semantics', 'source_evidence',
    'sql_source_dependency_verified', 'sql_column_dependencies',
    'sql_source_dependency_method', 'notice', 'warnings', 'page_context',
    'page_context_scope', 'extraction_verified',
})


def _number(value):
    if type(value) is float and not math.isfinite(value):
        raise ValueError('Non-finite result contract number')
    number = Decimal(str(value))
    if not number.is_finite() or abs(number.adjusted()) > 10000:
        raise ValueError('Result contract number exceeds encoding budget')
    if not number:
        return '0'
    text = format(number, 'f')
    return text.rstrip('0').rstrip('.') if '.' in text else text


def typed_sha256(value):
    digest = hashlib.sha256()

    def frame(tag, value):
        data = value.encode('utf-8')
        digest.update(tag + str(len(data)).encode('ascii') + b':' + data)

    def visit(item, depth=0):
        if depth > 80:
            raise ValueError('Result contract nesting exceeds budget')
        if item is None:
            digest.update(b'n;')
        elif type(item) is bool:
            digest.update(b'b1;' if item else b'b0;')
        elif type(item) in (int, float) or isinstance(item, Decimal):
            frame(b'd', _number(item))
        elif isinstance(item, str):
            frame(b's', item)
        elif isinstance(item, (list, tuple)):
            digest.update(b'a' + str(len(item)).encode('ascii') + b':')
            for child in item:
                visit(child, depth + 1)
        elif isinstance(item, dict) and all(isinstance(k, str) for k in item):
            digest.update(b'o' + str(len(item)).encode('ascii') + b':')
            for key in sorted(item):
                frame(b's', key)
                visit(item[key], depth + 1)
        else:
            raise ValueError('Unsupported result contract value')
    visit(value)
    return digest.hexdigest()


def unknown_unit(reason='No bound unit declaration for this expression'):
    return dict(status='unknown', evidence_level='none', binding_scope='result_column',
                evidence=[], reason=reason)


def declared_unit(symbol, evidence, *, level):
    if not isinstance(symbol, str) or not symbol.strip():
        return unknown_unit()
    if len(symbol.encode()) > 128:
        return unknown_unit('Unit declaration exceeds display budget')
    return dict(status='declared', symbol=symbol, evidence_level=level,
                binding_scope='result_column', evidence=deepcopy(evidence))


def snapshot(source, *, kind, versions=None, input_data=None, **identities):
    from .workspaces import current_identity
    identity=current_identity.get()
    kb=source.get('knowledge_base_id') or (identity.knowledge_base_id if identity else None)
    return dict(kind=kind, source_sha256=source['sha256'],
                file=source.get('file', source.get('file_name')),
                versions=versions or {}, input_digest_sha256=typed_sha256(input_data),
                **({'knowledge_base_id':kb} if kb else {}),
                **({'knowledge_id':source['knowledge_id']} if source.get('knowledge_id') else {}),
                **{k: v for k, v in identities.items() if v is not None})


def attach_result_contract(result, *, source_snapshot, execution, columns,
                           dependency_verified=False, source_cells=False):
    """Bind actual execution output and metadata without altering its values.

    source_cells describes the raw read's existing coordinates, not SQL result
    contributors, OCR correctness, or independently verified unit annotations.
    """
    if result.get('status') not in ('ok', 'no_data', 'reviewed', 'auto_accepted'):
        return result
    keys = sorted(HASH_INPUT_KEYS.intersection(result))
    identity = dict(payload={k:result[k] for k in keys}, source_snapshot=source_snapshot,
                    execution=execution, column_metadata=columns)
    contract = dict(schema_version=2, hash_algorithm=HASH_ALGORITHM,
                    result_id=result.get('result_id') or typed_sha256(identity)[:24],
                    hash_input_keys=keys, source_snapshot=deepcopy(source_snapshot),
                    execution=deepcopy(execution), column_metadata=deepcopy(columns),
                    evidence_capabilities=dict(
                        level='source_cells' if source_cells else 'dependency_only',
                        column_dependencies_verified=bool(dependency_verified),
                        cell_lineage_verified=False, source_cells_available=bool(source_cells),
                        aggregate_membership_verified=False))
    contract['result_hash'] = typed_sha256(dict(
        payload={k: result[k] for k in keys}, contract=contract))
    result['result_contract'] = contract
    return result


def source_column(name, index, logical_type, *, unit=None, dependency=None, display_name=None, operation=None):
    if dependency and dependency['kind']=='constant_label':
        unit=unknown_unit('Literal output labels have no source measurement unit')
    return dict(index=index, name=name, display_name=display_name or name,
                logical_type=logical_type, **({'operation':deepcopy(operation)} if operation is not None else {}), dependency=dependency or dict(
                    kind='source_value', source_columns=[name],
                    source_row_dependency=False, technical_metadata=False),
                unit=deepcopy(unit or unknown_unit()))


def sql_execution(sql, engine_version, numeric_semantics):
    return dict(kind='sql', engine='duckdb', engine_version=engine_version,
                numeric_semantics=numeric_semantics,
                sql_sha256=hashlib.sha256(sql.encode('utf-8')).hexdigest(),
                input_scope='loaded_inputs_before_user_sql', cell_lineage_verified=False)


def pdf_source_units(record):
    return {c['name']: declared_unit(c.get('unit'), [dict(
        source_column=c['name'], declared_unit=c.get('unit'),
        source_sha256=record['source']['sha256'], table_id=record['table_id'],
        plan_sha256=record['plan_sha256'],
        binding_scope='reviewed_logical_column_declaration',
        original_annotation_verified=False)], level='reviewed_plan_declaration')
        for c in record['columns']}


def excel_value_unit(series, source_sha256):
    units = [declared_unit(s.get('unit'), [dict(
        series_id=sid, declared_unit=s.get('unit'), source_sha256=source_sha256,
        sheet=s.get('sheet'), header_cells=s.get('header_cells', []),
        binding_scope='imported_series_declaration', original_annotation_verified=False)],
        level='imported_series_metadata') for sid, s in series.items()]
    if not units or any(u['status'] == 'unknown' for u in units):
        return unknown_unit('At least one loaded series has no unit declaration')
    if len({u['symbol'] for u in units}) != 1:
        return dict(status='mixed', evidence_level='none', binding_scope='result_column',
                    evidence=[], reason='Loaded value column contains different series units; SQL predicates are not unit bindings')
    result = deepcopy(units[0])
    result['evidence'] = [e for u in units for e in u['evidence']]
    return result
