"""Conservative expression dependencies for SQL over one authorized PDF table.

DuckDB's own parser supplies the AST. This binds column scopes, not SQL text
matches, and distinguishes an output value from a table mentioned elsewhere in
the query. It is deliberately not cell lineage, proof of algebraic influence,
or a judgement that a variable/axis answers the user's question.
"""
from __future__ import annotations

from dataclasses import dataclass, replace
import json


_ERROR = 'PDF SQL source dependency cannot be verified: '
_MAX_DEPENDENCY_NODE_VISITS = 25000


def _fail(message):
    raise ValueError(_ERROR + message)


@dataclass(frozen=True)
class _Dependency:
    columns: frozenset[str] = frozenset()
    cardinality: bool = False
    mixed_constants: bool = False
    null: bool = False
    elements_verified: bool = False

    @property
    def sourced(self):
        return bool(self.columns - {'_row_id'}) or self.cardinality

    @property
    def kind(self):
        if self.mixed_constants:
            return 'mixed_constant_values'
        if self.columns - {'_row_id'}:
            return 'source_value'
        if self.cardinality:
            return 'source_cardinality'
        if self.columns:
            return 'source_metadata'
        return 'constant_label'


def _combine(dependencies):
    dependencies = list(dependencies)
    return _Dependency(frozenset().union(*(d.columns for d in dependencies)),
                       any(d.cardinality for d in dependencies),
                       any(d.mixed_constants for d in dependencies),
                       bool(dependencies) and all(d.null for d in dependencies))


def _alternatives(dependencies):
    """Every numeric observation in a UNION/VALUES/list must remain sourced.

    A null branch is missing data, not an invented numeric observation. Labels
    that are constant in every branch remain explicitly constant labels.
    """
    dependencies = list(dependencies)
    result = _combine(dependencies)
    mixed = any(d.sourced for d in dependencies) and any(
        not d.sourced and not d.null for d in dependencies)
    return replace(result, mixed_constants=result.mixed_constants or mixed,
                   elements_verified=bool(dependencies) and all(d.elements_verified or d.null for d in dependencies))


@dataclass(frozen=True)
class _Column:
    name: str | None
    dependency: _Dependency
    qualifier: str = ''


@dataclass
class _Relation:
    columns: list[_Column]
    source_rows: bool = False
    mixed_rows: bool = False
    outer: _Relation | None = None


class _Binder:
    def __init__(self, names):
        self.source = _Relation([_Column(name, _Dependency(frozenset({name})), 'data')
                                 for name in ['_row_id', *names]], True)
        self.depth = 0
        self.node_visits = 0

    def _visit(self):
        # SQL text length bounds the parsed AST, but repeated CTE references can
        # expand into an exponential dependency traversal before SQL execution.
        self.node_visits += 1
        if self.node_visits > _MAX_DEPENDENCY_NODE_VISITS:
            _fail('dependency analysis node budget exceeded; simplify repeated CTE/subquery dependencies')

    def _lookup(self, names, scope):
        if not names or len(names) > 2:
            _fail('use unambiguous local column names or table aliases')
        name = names[-1].casefold()
        qualifier = names[-2].casefold() if len(names) == 2 else None
        columns = [c for c in scope.columns if c.name is not None and c.name.casefold() == name
                   and (qualifier is None or c.qualifier.casefold() == qualifier)]
        if not columns and scope.outer is not None:
            return self._lookup(names, scope.outer)
        if len(columns) != 1:
            _fail('unresolved or ambiguous column; give computed CTE columns explicit aliases')
        return columns[0].dependency

    def _alias(self, relation, node, default=''):
        aliases = node.get('column_name_alias', [])
        if len(aliases) > len(relation.columns):
            _fail('relation column aliases do not match its outputs')
        qualifier = node.get('alias') or default
        return _Relation([_Column(aliases[i] if i < len(aliases) else c.name,
                                  c.dependency, qualifier)
                          for i, c in enumerate(relation.columns)], relation.source_rows, relation.mixed_rows)

    def relation(self, node, ctes, outer):
        self._visit()
        kind = node.get('type')
        if kind == 'EMPTY':
            return _Relation([])
        if kind == 'BASE_TABLE':
            name = node.get('table_name', '').casefold()
            if node.get('catalog_name') or node.get('schema_name') not in (None, '', 'main'):
                _fail('only the scoped data table and local CTEs are allowed')
            if name in ctes:
                entry, environment = ctes[name]
                result = self.query(entry['query']['node'], environment, outer)
                aliases = entry.get('aliases', [])
                if aliases:
                    result = self._alias(result, {'column_name_alias': aliases})
            elif name == 'data':
                result = self.source
            else:
                _fail('only the scoped data table and local CTEs are allowed')
            return self._alias(result, node, name)
        if kind == 'SUBQUERY':
            return self._alias(self.query(node['subquery']['node'], ctes, outer), node)
        if kind == 'JOIN':
            left = self.relation(node['left'], ctes, outer)
            lateral = _Relation(left.columns, left.source_rows, left.mixed_rows, outer)
            right = self.relation(node['right'], ctes, lateral)
            # USING merges names in SELECT *. Explicit aliases avoid an incorrect
            # positional mapping until that shape has a separate binder.
            if node.get('using_columns') or node.get('ref_type') == 'NATURAL':
                _fail('use explicit JOIN ON and qualified projection columns')
            return _Relation([*left.columns, *right.columns], left.source_rows or right.source_rows,
                             left.mixed_rows or right.mixed_rows)
        if kind == 'EXPRESSION_LIST':
            rows = node.get('values', [])
            if not rows or any(len(r) != len(rows[0]) for r in rows):
                _fail('VALUES requires a rectangular explicit projection')
            columns = [_Column('col' + str(i), _alternatives(
                self.expression(row[i], outer, ctes) for row in rows)) for i in range(len(rows[0]))]
            return self._alias(_Relation(columns), node)
        if kind == 'TABLE_FUNCTION':
            function = node.get('function', {})
            if function.get('function_name', '').casefold() != 'unnest':
                _fail('external, catalog, dynamic SQL and generated table functions are not source data')
            children = function.get('children', [])
            if len(children) != 1 or node.get('with_ordinality') not in (None, 'WITHOUT_ORDINALITY'):
                _fail('use SELECT unnest(source_expression) with explicit aliases')
            dep = self.expression(children[0], outer, ctes)
            if not dep.elements_verified:
                _fail('UNNEST requires verified source-element arrays; use explicit source columns or list aggregates')
            return self._alias(_Relation([_Column('unnest', dep)], bool(dep.columns) or dep.cardinality), node, 'unnest')
        if kind == 'PIVOT':
            return self.unpivot(node, ctes, outer)
        _fail('unsupported relation; use scoped SELECT/CTE, explicit joins, VALUES or UNPIVOT')

    def unpivot(self, node, ctes, outer):
        source = self.relation(node['source'], ctes, outer)
        pivots = node.get('pivots', [])
        values = node.get('unpivot_names', [])
        if len(pivots) != 1 or len(values) != 1 or len(pivots[0].get('unpivot_names', [])) != 1:
            _fail('use a single explicit UNPIVOT value column or source-based SELECT unnest arrays')
        pivot = pivots[0]
        dependencies, names = [], set()
        for entry in pivot.get('entries', []):
            items = entry.get('values', [])
            if len(items) != 1 or not isinstance(items[0].get('value'), str):
                _fail('UNPIVOT requires explicit existing source column names')
            name = items[0]['value']
            dependencies.append(self._lookup([name], source))
            names.add(name.casefold())
        if not dependencies:
            _fail('UNPIVOT requires explicit existing source column names')
        columns = [c for c in source.columns if c.name is None or c.name.casefold() not in names]
        columns += [_Column(pivot['unpivot_names'][0], _Dependency()),
                    _Column(values[0], _alternatives(dependencies))]
        return self._alias(_Relation(columns, source.source_rows, source.mixed_rows), node)

    def _children(self, value, scope, ctes):
        if isinstance(value, dict):
            if 'class' in value:
                yield self.expression(value, scope, ctes)
            else:
                for item in value.values():
                    yield from self._children(item, scope, ctes)
        elif isinstance(value, list):
            for item in value:
                yield from self._children(item, scope, ctes)

    def expression(self, node, scope, ctes):
        self._visit()
        kind = node.get('class')
        if kind == 'CONSTANT':
            return _Dependency(null=node.get('value', {}).get('is_null', False))
        if kind == 'COLUMN_REF':
            return self._lookup(node.get('column_names', []), scope)
        if kind == 'SUBQUERY':
            result = self.query(node['subquery']['node'], ctes, scope)
            subquery_type = node.get('subquery_type')
            if subquery_type in ('EXISTS', 'NOT_EXISTS'):
                return _Dependency(cardinality=result.source_rows)
            if subquery_type != 'SCALAR' or len(result.columns) != 1:
                _fail('use explicit scalar subqueries or joins for source expressions')
            return result.columns[0].dependency
        if kind in ('FUNCTION', 'WINDOW'):
            name = node.get('function_name', '').casefold()
            # A scalar dependency set cannot prove which field a composite
            # extractor returns. Packing a real field next to an invented one
            # must not let the invented field inherit the real field's source.
            # These are SQL type/function protocols, not question intent rules.
            if name.startswith(('struct_', 'map_', 'union_', 'json_')) or name in (
                    'row', 'map', 'to_json', 'from_json', 'from_json_strict', 'array_to_json', 'row_to_json'):
                _fail('composite construction/field extraction is unsupported; use explicit scalar source projections')
            children = node.get('children', [])
            values = [self.expression(child, scope, ctes) for child in children]
            dependencies = [*values, *self._children({k: v for k, v in node.items()
                if k not in ('class', 'type', 'children')}, scope, ctes)]
            if name in ('list_value', 'array_value'):
                result = _alternatives(values)
                return replace(result, elements_verified=True, null=not values or result.null)
            if name in ('list', 'array_agg'):
                # A filter/order affects which elements are selected, not the
                # values of literal elements. It cannot source a constant list.
                if len(values) != 1:
                    _fail('list aggregates require one explicit value expression')
                return replace(values[0], elements_verified=True)
            if name in ('list_concat', 'array_concat'):
                return _alternatives(values)
            if name in ('list_append', 'array_append', 'list_prepend', 'array_prepend'):
                if len(values) != 2:
                    _fail('list append/prepend requires explicit array and value arguments')
                array = values[1] if name.endswith('prepend') else values[0]
                return replace(_alternatives(values), elements_verified=array.elements_verified)
            if name in ('list_resize', 'array_resize'):
                if len(values) not in (2, 3):
                    _fail('array resize requires explicit array, length and optional fill value')
                # Length is a parameter, not a source for the appended values.
                # The default fill is NULL; a literal numeric fill mixed with
                # real source elements must never become a sourced observation.
                fill = values[2] if len(values) == 3 else _Dependency(null=True)
                return replace(_alternatives([values[0], fill]), elements_verified=values[0].elements_verified)
            if name == 'unnest':
                if len(values) != 1 or not values[0].elements_verified:
                    _fail('UNNEST requires verified source-element arrays; use explicit source columns or list aggregates')
                return values[0]
            result = _combine(dependencies)
            if name in ('count', 'count_star', 'sum', 'sum_no_overflow') and scope.source_rows:
                result = replace(result, cardinality=True,
                                 mixed_constants=result.mixed_constants or scope.mixed_rows)
                if name in ('count', 'count_star'):
                    # Counting real row identities is a row statistic, not an
                    # attempt to use the identities themselves as measurements.
                    result = replace(result, columns=result.columns - {'_row_id'})
            if kind == 'WINDOW' and node.get('type') in (
                    'WINDOW_ROW_NUMBER', 'WINDOW_RANK', 'WINDOW_RANK_DENSE', 'WINDOW_PERCENT_RANK',
                    'WINDOW_CUME_DIST', 'WINDOW_NTILE') and scope.source_rows:
                result = replace(result, cardinality=True)
            return result
        if kind in ('CAST', 'COMPARISON', 'CONJUNCTION', 'OPERATOR', 'CASE', 'COLLATE', 'BETWEEN'):
            dependencies = list(self._children({k: v for k, v in node.items()
                if k not in ('class', 'type')}, scope, ctes))
            result = _combine(dependencies)
            if kind == 'CAST' and len(dependencies) == 1:
                result = replace(result, elements_verified=dependencies[0].elements_verified)
            return result
        _fail('unsupported expression; use explicit source columns, casts and deterministic calculations')

    def star(self, node, scope, ctes):
        # COLUMNS with a regex/lambda needs a separate expansion binder; a plain
        # COLUMNS(*) has exactly the same positional projection as *.
        if node.get('expr') or node.get('qualified_exclude_list'):
            _fail('expand COLUMNS expressions into explicit source columns')
        qualifier = node.get('relation_name', '').casefold()
        excluded = {n.casefold() for n in node.get('exclude_list', [])}
        replacements = {v['key'].casefold(): v['value'] for v in node.get('replace_list', [])}
        renames = {v['key']['column'].casefold(): v['value'] for v in node.get('rename_list', [])}
        columns = []
        for c in scope.columns:
            if qualifier and c.qualifier.casefold() != qualifier:
                continue
            if c.name is None:
                if excluded or replacements or renames:
                    _fail('give computed subquery columns explicit aliases before using star modifiers')
                columns.append(c)
                continue
            if c.name.casefold() in excluded:
                continue
            dep = self.expression(replacements[c.name.casefold()], scope, ctes) if c.name.casefold() in replacements else c.dependency
            columns.append(_Column(renames.get(c.name.casefold(), c.name), dep))
        return columns

    def query(self, node, ctes=None, outer=None):
        self._visit()
        self.depth += 1
        if self.depth > 40:
            _fail('recursive/deep query dependencies are unsupported')
        try:
            ctes, outer = dict(ctes or {}), outer or _Relation([])
            # Capture each definition's lexical environment. A same-name CTE
            # cannot accidentally resolve to the authorized physical table.
            for entry in node.get('cte_map', {}).get('map', []):
                key = entry['key'].casefold()
                environment = dict(ctes)
                environment[key] = (entry['value'], environment)
                ctes[key] = (entry['value'], environment)
            kind = node.get('type')
            if kind == 'SET_OPERATION_NODE':
                left, right = self.query(node['left'], ctes, outer), self.query(node['right'], ctes, outer)
                if len(left.columns) != len(right.columns) or node.get('setop_type') not in ('UNION', 'EXCEPT', 'INTERSECT'):
                    _fail('use a positional UNION/EXCEPT/INTERSECT with matching columns')
                if node.get('setop_type') != 'UNION':
                    return left
                columns = [_Column(a.name, _alternatives([a.dependency, b.dependency]))
                           for a, b in zip(left.columns, right.columns)]
                return _Relation(columns, left.source_rows or right.source_rows,
                                 left.mixed_rows or right.mixed_rows or left.source_rows != right.source_rows)
            if kind != 'SELECT_NODE':
                _fail('only non-recursive SELECT/WITH queries are supported')
            relation = self.relation(node.get('from_table', {'type': 'EMPTY'}), ctes, outer)
            scope = _Relation(list(relation.columns), relation.source_rows, relation.mixed_rows, outer)
            columns = []
            for expression in node.get('select_list', []):
                if expression.get('class') == 'STAR':
                    columns.extend(self.star(expression, relation, ctes))
                else:
                    name = expression.get('alias') or (expression.get('column_names', [None])[-1]
                                                       if expression.get('class') == 'COLUMN_REF' else None)
                    column = _Column(name, self.expression(expression, scope, ctes))
                    columns.append(column)
                    if expression.get('alias') and not any(c.name and c.name.casefold() == name.casefold() for c in scope.columns):
                        scope.columns.append(column)
            return _Relation(columns, relation.source_rows, relation.mixed_rows)
        finally:
            self.depth -= 1


def verify_pdf_sql_dependencies(connection, sql, source_columns):
    """Return per-output dependencies, rejecting results with no source values.

    Constant columns can be dimension/series labels; consumers must never use
    them as sourced numeric observations. Every UNION/VALUES/array alternative
    is classified, so a genuine source branch cannot launder literal values.
    The connection must remain isolated with external access disabled.
    """
    serialized = connection.execute(
        'SELECT json_serialize_sql(?, skip_empty := true, skip_null := true)', [sql]).fetchone()[0]
    parsed = json.loads(serialized)
    if parsed.get('error') or len(parsed.get('statements', [])) != 1:
        _fail('DuckDB could not parse one SELECT statement')
    def validate_relations(value):
        # Inspect even unused CTEs, predicates and aggregate filters. An unused
        # external relation must not escape validation just because it does not
        # contribute to the selected output expressions.
        if isinstance(value, dict):
            if value.get('type') == 'BASE_TABLE' and (
                    value.get('catalog_name') or value.get('schema_name') not in (None, '', 'main')):
                _fail('only the scoped data table and local CTEs are allowed')
            if value.get('type') == 'TABLE_FUNCTION' and value.get('function', {}).get('function_name', '').casefold() != 'unnest':
                _fail('external, catalog, dynamic SQL and generated table functions are not source data')
            for child in value.values():
                validate_relations(child)
        elif isinstance(value, list):
            for child in value:
                validate_relations(child)
    validate_relations(parsed)
    result = _Binder(source_columns).query(parsed['statements'][0]['node'])
    if not any(c.dependency.sourced and not c.dependency.mixed_constants for c in result.columns):
        _fail('outputs must read real source fields or compute source-row statistics; '
              'literal observations, unused CTEs and row IDs are not source values')
    return [dict(column_index=i, kind=c.dependency.kind,
                 source_columns=sorted(c.dependency.columns), source_row_dependency=c.dependency.cardinality,
                 technical_metadata='_row_id' in c.dependency.columns)
            for i, c in enumerate(result.columns)]
