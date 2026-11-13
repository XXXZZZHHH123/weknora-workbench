"""Describe executed SQL projections, independently of model-chosen aliases.

This is syntax metadata bound by the result contract, not business intent or
cell lineage. Unknown/subquery shapes are explicit; SQL is never rewritten.
"""
import json
import duckdb


def _expression(node, depth=0):
    if depth > 8:
        return {"kind": "unknown", "reason": "expression_depth_budget"}
    kind = node.get("class")
    if kind == "COLUMN_REF":
        return {"kind": "column_reference", "names": node.get("column_names", []),
                "scope": "sql_relation_not_original_cell"}
    if kind == "CONSTANT":
        return {"kind": "literal", "source_measurement": False}
    if kind == "FUNCTION":
        name = node.get("function_name", "").lower()
        children = node.get("children", [])
        if len(children) > 16:
            return {"kind": "unknown", "reason": "argument_budget"}
        result = {"kind": "function", "name": name,
                  "arguments": [_expression(c, depth+1) for c in children]}
        if node.get("filter") or node.get("order_bys") or node.get("distinct"):
            result["modifiers"] = "consult_executed_sql"
        # SQL operator definitions only; never infer a calendar, measure or unit.
        if name in ("arg_max", "arg_min", "max_by", "min_by") and len(children) == 2:
            result["meaning"] = "first_argument_at_extreme_of_second_argument"
            result["ordering"] = "maximum" if name in ("arg_max", "max_by") else "minimum"
            result["notice"] = "The returned first argument is not necessarily its own maximum/minimum. Ties and NULLs follow the executed DuckDB operator."
        elif name in ("max", "min") and len(children) == 1:
            result["meaning"] = "extreme_of_argument_itself"
        return result
    return {"kind": "unknown", "reason": "unsupported_expression_shape"}


def describe_sql_operations(connection, sql, output_count):
    unknown = lambda: [{"kind": "unknown", "reason": "projection_shape_unresolved"}
                      for _ in range(output_count)]
    try:
        parsed = json.loads(connection.execute(
            "SELECT json_serialize_sql(?, skip_empty := true, skip_null := true)", [sql]).fetchone()[0])
        statements = parsed.get("statements", [])
        if parsed.get("error") or len(statements) != 1:
            return unknown()
        node = statements[0]["node"]
        projections = node.get("select_list", [])
        if node.get("type") != "SELECT_NODE" or len(projections) != output_count or any(
                p.get("class") == "STAR" for p in projections):
            return unknown()
        result = [_expression(p) for p in projections]
        if len(json.dumps(result)) > 16000:
            return unknown()
        return result
    except (KeyError, TypeError, ValueError, RecursionError, duckdb.Error):
        return unknown()
