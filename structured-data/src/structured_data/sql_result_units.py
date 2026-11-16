"""Conservative unit propagation over DuckDB's AST, never SQL rewriting.

Unknown syntax remains executable by the existing isolated SQL engine, but has
unknown units unless its operands already carry a known unit conflict.
A declaration is not an independently verified page annotation.
No unit is inferred from question/alias/value magnitude or a same-page note.
"""
from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass
import json
import duckdb

from .result_contract import mixed_unit, unknown_unit


@dataclass
class _Value:
    unit: dict
    name: str | None = None
    qualifier: str = ''
    scalar: bool = False
    null: bool = False


def _unknown(reason='SQL shape has no supported deterministic unit rule', values=()):
    # Lack of an operation rule is not evidence that a known conflict vanished.
    # This only preserves a hazard; it never supplies a unit to unknown SQL.
    if any(v.unit['status'] == 'mixed' for v in values):
        return _Value(mixed_unit([v.unit for v in values],
                                'SQL expression retains a known mixed-unit input; no common output unit is proven'))
    return _Value(unknown_unit(reason))


def _derived(value, operation):
    if value.unit['status'] not in ('declared', 'derived'):
        return _unknown(values=[value])
    unit = deepcopy(value.unit)
    unit.update(status='derived', evidence_level='deterministic_expression',
                expression=operation)
    return _Value(unit)


def _same(values, *, operation='alternatives'):
    values = [v for v in values if not v.null]
    if not values:
        return _unknown()
    symbols = {v.unit['symbol'] for v in values if v.unit['status'] in ('declared', 'derived')}
    if any(v.unit['status'] == 'mixed' for v in values) or len(symbols) > 1:
        return _Value(mixed_unit([v.unit for v in values],
                                'Expression alternatives retain different explicit units'))
    if any(v.unit['status'] not in ('declared', 'derived') for v in values):
        return _unknown('An expression branch has no bound unit declaration')
    unit = _derived(values[0], operation).unit
    unit['evidence'] = []
    for value in values:
        for entry in value.unit.get('evidence', []):
            if entry not in unit['evidence']:
                unit['evidence'].append(deepcopy(entry))
    return _Value(unit)


class _Units:
    def __init__(self, source):
        self.source = [_Value(deepcopy(unit), name, 'data') for name, unit in source.items()]
        self.visits = 0

    def visit(self):
        self.visits += 1
        if self.visits > 10000:
            raise ValueError('Unit analysis budget exceeded')

    def alias(self, values, node, default=''):
        names = node.get('column_name_alias', [])
        return [_Value(v.unit, names[i] if i < len(names) else v.name,
                       node.get('alias') or default, v.scalar, v.null)
                for i, v in enumerate(values)]

    def lookup(self, names, scope):
        if not names or len(names) > 2:
            return _unknown()
        matches = [v for v in scope if v.name and v.name.casefold() == names[-1].casefold()
                   and (len(names) == 1 or v.qualifier.casefold() == names[-2].casefold())]
        return deepcopy(matches[0]) if len(matches) == 1 else _unknown()

    def relation(self, node, ctes):
        self.visit()
        kind = node.get('type')
        if kind == 'EMPTY':
            return []
        if kind == 'BASE_TABLE':
            name = node.get('table_name', '').casefold()
            if name in ctes:
                entry, environment = ctes[name]
                values = self.query(entry['query']['node'], environment)
                values = self.alias(values, {'column_name_alias': entry.get('aliases', [])})
            elif name == 'data':
                values = deepcopy(self.source)
            else:
                return []
            return self.alias(values, node, name)
        if kind == 'SUBQUERY':
            return self.alias(self.query(node['subquery']['node'], ctes), node)
        if kind == 'JOIN':
            if node.get('using_columns') or node.get('ref_type') == 'NATURAL':
                return []
            return self.relation(node['left'], ctes) + self.relation(node['right'], ctes)
        if kind == 'PIVOT':
            source = self.relation(node['source'], ctes)
            pivots = node.get('pivots', [])
            if len(pivots) != 1 or len(node.get('unpivot_names', [])) != 1:
                return []
            pivot = pivots[0]
            names = [e['values'][0]['value'] for e in pivot.get('entries', [])
                     if len(e.get('values', [])) == 1 and isinstance(e['values'][0].get('value'), str)]
            if not names or len(pivot.get('unpivot_names', [])) != 1:
                return []
            values = [v for v in source if not v.name or v.name.casefold() not in {n.casefold() for n in names}]
            label = _unknown('UNPIVOT output labels are generated column labels, not measurements')
            label.name = pivot['unpivot_names'][0]
            observed = _same([self.lookup([name], source) for name in names], operation='unpivot_alternatives')
            observed.name = node['unpivot_names'][0]
            return self.alias([*values, label, observed], node)
        # Collection element units, correlated VALUES and dynamic pivots need
        # element/branch scopes. Do not launder them through a dependency set.
        return []

    def expression(self, node, scope, ctes):
        self.visit()
        kind = node.get('class')
        if kind == 'COLUMN_REF':
            return self.lookup(node.get('column_names', []), scope)
        if kind == 'CONSTANT':
            value = node.get('value', {})
            return _Value(unknown_unit('Literal labels/parameters have no source measurement unit'),
                          scalar=value.get('type', {}).get('id') in (
                              'TINYINT', 'SMALLINT', 'INTEGER', 'BIGINT', 'HUGEINT', 'UTINYINT',
                              'USMALLINT', 'UINTEGER', 'UBIGINT', 'UHUGEINT', 'DECIMAL', 'FLOAT', 'DOUBLE'),
                          null=value.get('is_null', False))
        if kind == 'SUBQUERY' and node.get('subquery_type') == 'SCALAR':
            values = self.query(node['subquery']['node'], ctes)
            return values[0] if len(values) == 1 else _unknown()
        if kind == 'CAST':
            value = self.expression(node['child'], scope, ctes)
            target = node.get('cast_type', {}).get('id', '')
            if target in ('DECIMAL', 'DOUBLE', 'FLOAT', 'TINYINT', 'SMALLINT', 'INTEGER',
                          'BIGINT', 'HUGEINT', 'UTINYINT', 'USMALLINT', 'UINTEGER', 'UBIGINT', 'UHUGEINT'):
                return _Value(_derived(value, 'numeric_cast_no_unit_conversion').unit, scalar=value.scalar, null=value.null)
            return _unknown('Cast target has no supported numeric unit rule', [value])
        if kind == 'CASE':
            branches = [self.expression(x['then_expr'], scope, ctes) for x in node.get('case_checks', [])]
            if node.get('else_expr'):
                branches.append(self.expression(node['else_expr'], scope, ctes))
            return _same(branches, operation='case_alternatives')
        if kind in ('FUNCTION', 'WINDOW'):
            name = node.get('function_name', '').casefold()
            values = [self.expression(child, scope, ctes) for child in node.get('children', [])]
            if name in ('count', 'count_star'):
                return _Value(dict(status='derived', symbol='1',
                                   evidence_level='deterministic_expression', binding_scope='result_column',
                                   evidence=[], expression='sql_count', quantity='record_count'))
            if name in ('sum', 'avg', 'mean', 'min', 'max', 'abs', 'round', 'floor', 'ceil', 'ceiling') and values:
                # A unit does not certify additivity or a business calendar.
                return _derived(values[0], 'sql_' + name)
            if name in ('+', '-') and len(values) == 1:
                return _derived(values[0], 'unary_' + name)
            if name in ('+', '-') and len(values) == 2:
                return _same(values, operation='arithmetic_' + name)
            if name == '*' and len(values) == 2:
                if values[0].scalar and not values[1].scalar:
                    return _derived(values[1], 'scalar_multiply_no_unit_conversion')
                if values[1].scalar and not values[0].scalar:
                    return _derived(values[0], 'scalar_multiply_no_unit_conversion')
            if name == '/' and len(values) == 2:
                if values[1].scalar:
                    return _derived(values[0], 'scalar_divide_no_unit_conversion')
                both = _same(values, operation='ratio_operands')
                if both.unit['status'] == 'derived':
                    unit = both.unit
                    unit.update(symbol='1', expression='ratio_of_same_explicit_unit', quantity='ratio')
                    return _Value(unit)
            if name in ('coalesce', 'ifnull', 'greatest', 'least'):
                return _same(values, operation=name + '_alternatives')
            return _unknown(values=values)
        # DuckDB also represents operations such as COALESCE as OPERATOR nodes.
        # Unhandled operations may retain conflicts, but cannot derive units.
        return _unknown(values=list(self.children(node, scope, ctes)))

    def children(self, node, scope, ctes):
        for value in node.values():
            if isinstance(value, dict):
                if 'class' in value:
                    yield self.expression(value, scope, ctes)
                else:
                    yield from self.children(value, scope, ctes)
            elif isinstance(value, list):
                for item in value:
                    if isinstance(item, dict):
                        if 'class' in item:
                            yield self.expression(item, scope, ctes)
                        else:
                            yield from self.children(item, scope, ctes)

    def query(self, node, ctes=None):
        self.visit()
        ctes = dict(ctes or {})
        for entry in node.get('cte_map', {}).get('map', []):
            environment = dict(ctes)
            environment[entry['key'].casefold()] = (entry['value'], environment)
            ctes[entry['key'].casefold()] = (entry['value'], environment)
        if node.get('type') == 'SET_OPERATION_NODE':
            left, right = self.query(node['left'], ctes), self.query(node['right'], ctes)
            if len(left) != len(right):
                return []
            if node.get('setop_type') != 'UNION':
                return left
            result = []
            for a, b in zip(left, right):
                value = _same([a, b], operation='union_alternatives')
                value.name = a.name
                result.append(value)
            return result
        if node.get('type') != 'SELECT_NODE':
            return []
        relation = self.relation(node.get('from_table', {'type': 'EMPTY'}), ctes)
        scope, result = list(relation), []
        for node in node.get('select_list', []):
            if node.get('class') == 'STAR':
                # Complex STAR EXCLUDE/REPLACE cannot reuse positional units.
                if node.get('exclude_list') or node.get('replace_list') or node.get('rename_list'):
                    return []
                result.extend(deepcopy([v for v in relation if not node.get('relation_name')
                                       or v.qualifier.casefold() == node['relation_name'].casefold()]))
            else:
                value = self.expression(node, scope, ctes)
                value.name = node.get('alias') or (node.get('column_names', [None])[-1]
                                                   if node.get('class') == 'COLUMN_REF' else None)
                result.append(value)
                if node.get('alias') and not any(v.name and v.name.casefold() == value.name.casefold() for v in scope):
                    scope.append(value)
        return result


def _bound_output_columns(node, source_columns):
    """Reuse the scope binder without changing its source/label guarantees."""
    from .pdf_sql_provenance import _Binder, _Column, _Dependency, _Relation
    binder = _Binder([])
    binder.source = _Relation([_Column(name, _Dependency(frozenset({name})), 'data')
                               for name in source_columns], True)
    return binder.query(node).columns


def _retain_mixed_dependencies(units, node, source_units):
    """Unknown unit syntax cannot erase an independently bound source conflict.

    This fallback never certifies a measurement unit or an unverified SQL scope.
    Supported count/ratio rules keep their dimensionless result. Unsupported
    relations remain unverified by the same binder used for result provenance.
    """
    mixed = {name: unit for name, unit in source_units.items() if unit['status'] == 'mixed'}
    if not mixed or not any(unit['status'] == 'unknown' for unit in units):
        return units
    try:
        columns = _bound_output_columns(node, source_units)
        if len(columns) != len(units):
            return units
        retained = []
        for unit, column in zip(units, columns):
            conflicts = [mixed[name] for name in sorted(column.dependency.columns) if name in mixed]
            if unit['status'] == 'unknown' and conflicts:
                unit = mixed_unit(conflicts, 'SQL output depends on known mixed-unit source values; no common output unit is proven')
            retained.append(unit)
        return retained
    except (KeyError, TypeError, ValueError, RecursionError):
        return units


def infer_sql_units(connection, sql, source_units, output_count):
    """Never changes/reexecutes SQL and never rejects safe SQL for unit limits."""
    try:
        parsed = json.loads(connection.execute(
            'SELECT json_serialize_sql(?, skip_empty := true, skip_null := true)', [sql]).fetchone()[0])
        if parsed.get('error') or len(parsed.get('statements', [])) != 1:
            return [unknown_unit() for _ in range(output_count)]
        node = parsed['statements'][0]['node']
        values = _Units(source_units).query(node)
        units = ([v.unit for v in values] if len(values) == output_count else
                 [unknown_unit('Unit analysis is incomplete for this SQL shape') for _ in range(output_count)])
        return _retain_mixed_dependencies(units, node, source_units)
    except (KeyError, TypeError, ValueError, RecursionError, duckdb.Error):
        pass
    return [unknown_unit('Unit analysis is incomplete for this SQL shape') for _ in range(output_count)]


def describe_excel_dependencies(connection, sql, source_columns, output_count):
    """Share the tested column-scope binder; unsupported SQL stays unverified.

    This does not authorize SQL or change Excel's existing execution contract.
    Physical coordinate/identity columns remain technical even through aliases.
    """
    try:
        parsed = json.loads(connection.execute(
            'SELECT json_serialize_sql(?, skip_empty := true, skip_null := true)', [sql]).fetchone()[0])
        if parsed.get('error') or len(parsed.get('statements', [])) != 1:
            raise ValueError('Unparsed SQL')
        outputs = _bound_output_columns(parsed['statements'][0]['node'], source_columns)
        if len(outputs) != output_count:
            raise ValueError('Dependency shape differs')
        technical = {'series_id', 'source_cell', 'date_cell', 'sheet'}
        dependencies = []
        for value in outputs:
            dep = value.dependency
            metadata = bool(dep.columns & technical)
            dependencies.append(dict(kind=dep.kind, source_columns=sorted(dep.columns),
                                     source_row_dependency=dep.cardinality,
                                     technical_metadata=metadata))
        return dependencies, True
    except (KeyError, TypeError, ValueError, RecursionError, duckdb.Error):
        return [dict(kind='unknown', source_columns=[], source_row_dependency=False,
                     technical_metadata=False) for _ in range(output_count)], False
