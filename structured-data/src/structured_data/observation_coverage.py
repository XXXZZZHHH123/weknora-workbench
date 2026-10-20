"""Bounded evidence about status runs among imported observation records.

Runs describe adjacent selected rows in source-date order only. They do not
assert a daily/monthly cadence and never synthesize absent source rows.
"""

MAX_COVERAGE_SEGMENTS = 64
_TABLES = {"observations", "data"}


def summarize_observation_coverage(con, *, table, where="TRUE", params=(), max_segments=MAX_COVERAGE_SEGMENTS):
    """Summarize value-status runs over a complete filtered input, before paging.

    ``table`` is restricted to internal fixed tables; ``where`` is composed by
    trusted callers. Null dates and duplicate dates are isolated as boundaries
    because they do not provide an unambiguous ordering between observations.
    """
    if table not in _TABLES:
        raise ValueError("unsupported observation table")
    if type(max_segments) is not int or not 1 <= max_segments <= MAX_COVERAGE_SEGMENTS:
        raise ValueError("invalid segment bound")

    count, valid_count, first, last = con.execute(
        f"SELECT count(*), count(value), min(date), max(date) FROM {table} WHERE {where}",
        list(params),
    ).fetchone()
    sql = f"""
        WITH selected AS (
          SELECT date, source_cell, value, value_status FROM {table} WHERE {where}
        ), dated AS (
          SELECT *, count(*) OVER (PARTITION BY date) AS same_date_count
          FROM selected
        ), ordered AS (
          SELECT *, row_number() OVER w AS rn,
            lag(date) OVER w AS prev_date,
            lag(value_status) OVER w AS prev_status,
            lag(value IS NOT NULL) OVER w AS prev_has_value,
            lag(same_date_count) OVER w AS prev_same_date_count
          FROM dated WINDOW w AS (ORDER BY date NULLS LAST, source_cell)
        ), marked AS (
          SELECT *, CASE
            WHEN rn=1 OR date IS NULL OR prev_date IS NULL THEN 1
            WHEN same_date_count>1 OR coalesce(prev_same_date_count, 0)>1 THEN 1
            WHEN value_status IS DISTINCT FROM prev_status THEN 1
            WHEN (value IS NOT NULL) IS DISTINCT FROM prev_has_value THEN 1
            ELSE 0 END AS starts_segment
          FROM ordered
        ), grouped AS (
          SELECT *, sum(starts_segment) OVER (ORDER BY rn ROWS UNBOUNDED PRECEDING) AS segment_id
          FROM marked
        ), segments AS (
          SELECT segment_id, min(rn) AS first_rn, max(rn) AS last_rn,
            min(date) AS start_date, max(date) AS end_date,
            count(*) AS record_count, count(value) AS valid_count,
            first(value_status ORDER BY rn) AS value_status,
            first(source_cell ORDER BY rn) AS first_cell,
            last(source_cell ORDER BY rn) AS last_cell,
            bool_or(same_date_count>1) AS has_duplicate_date,
            bool_or(date IS NULL) AS has_undated_record
          FROM grouped GROUP BY segment_id
        )
        SELECT *, count(*) OVER () AS total_segments
        FROM segments ORDER BY first_rn LIMIT ?
    """
    rows = con.execute(sql, list(params) + [max_segments]).fetchall()
    segments = []
    total_segments = rows[0][-1] if rows else 0
    for row in rows:
        (_, _, _, start, end, record_count, valid, status, first_cell, last_cell,
         duplicate, undated, _) = row
        segments.append({
            "start_date": str(start) if start is not None else None,
            "end_date": str(end) if end is not None else None,
            "record_count": record_count,
            "valid_count": valid,
            "value_status": status,
            "has_value": valid > 0,
            "first_cell": first_cell,
            "last_cell": last_cell,
            "duplicate_date_boundary": bool(duplicate),
            "undated_boundary": bool(undated),
        })

    change_sql = f"""
        WITH selected AS (
          SELECT date, source_cell, value FROM {table} WHERE {where}
        ), dated AS (
          SELECT *, count(*) OVER (PARTITION BY date) AS same_date_count
          FROM selected
        ), ordered AS (
          SELECT *, row_number() OVER w AS rn,
            lag(date) OVER w AS prev_date,
            lag(value) OVER w AS prev_value,
            lag(same_date_count) OVER w AS prev_same_date_count
          FROM dated WINDOW w AS (ORDER BY date NULLS LAST, source_cell)
        ), classified AS (
          SELECT *,
            date IS NOT NULL AND same_date_count=1 AND value IS NOT NULL AS eligible,
            date IS NOT NULL AND same_date_count=1 AND value IS NOT NULL
              AND prev_date IS NOT NULL AND prev_same_date_count=1 AND prev_value IS NOT NULL AS comparable
          FROM ordered
        )
        SELECT
          count(*) FILTER (WHERE eligible) AS eligible_count,
          count(*) FILTER (WHERE comparable) AS compared_pair_count,
          count(*) FILTER (WHERE comparable AND value > prev_value) AS increase_count,
          count(*) FILTER (WHERE comparable AND value < prev_value) AS decrease_count,
          count(*) FILTER (WHERE comparable AND value = prev_value) AS unchanged_count,
          first(date ORDER BY date, source_cell) FILTER (WHERE eligible) AS first_date,
          first(value ORDER BY date, source_cell) FILTER (WHERE eligible) AS first_value,
          last(date ORDER BY date, source_cell) FILTER (WHERE eligible) AS last_date,
          last(value ORDER BY date, source_cell) FILTER (WHERE eligible) AS last_value,
          first(date ORDER BY value, date, source_cell) FILTER (WHERE eligible) AS minimum_date,
          min(value) FILTER (WHERE eligible) AS minimum_value,
          first(date ORDER BY value DESC, date, source_cell) FILTER (WHERE eligible) AS maximum_date,
          max(value) FILTER (WHERE eligible) AS maximum_value,
          count(*) FILTER (WHERE date IS NULL) AS null_date_count,
          count(*) FILTER (WHERE value IS NULL) AS missing_value_count,
          count(*) FILTER (WHERE date IS NOT NULL AND same_date_count>1) AS duplicate_date_record_count
        FROM classified
    """
    change_row = con.execute(change_sql, list(params)).fetchone()
    (
        eligible_count, compared_pair_count, increase_count, decrease_count,
        unchanged_count, first_date, first_value, last_date, last_value,
        minimum_date, minimum_value, maximum_date, maximum_value,
        null_date_count, missing_value_count, duplicate_date_record_count,
    ) = change_row

    def date_text(value):
        return str(value) if value is not None else None

    value_changes = {
        "scope": {
            "record_count": count,
            "valid_value_count": valid_count,
            "eligible_record_count": eligible_count,
            "eligible_start_date": date_text(first_date),
            "eligible_end_date": date_text(last_date),
            "complete_filtered_input": True,
        },
        "first": {"date": date_text(first_date), "value": first_value},
        "last": {"date": date_text(last_date), "value": last_value},
        "minimum": {"date": date_text(minimum_date), "value": minimum_value},
        "maximum": {"date": date_text(maximum_date), "value": maximum_value},
        "adjacent_comparison_count": compared_pair_count,
        "increase_count": increase_count,
        "decrease_count": decrease_count,
        "unchanged_count": unchanged_count,
        "excluded_boundary_records": {
            "null_date_count": null_date_count,
            "missing_value_count": missing_value_count,
            "duplicate_date_record_count": duplicate_date_record_count,
        },
        "interpretation": (
            "比较仅覆盖筛选范围内按日期及来源单元格排序后相邻、且日期非空唯一、数值非空的记录对；"
            "空日期、重复日期和缺值记录会中断比较。first/last是符合资格的范围端点，不表示端点间没有断点。"
            "极值日期只给出符合资格记录中的首次出现，不代表极值唯一。未假定采样频率，不推断未出现在输入中的日期或记录。"
        ),
    }
    return {
        "scope_record_count": count,
        "scope_valid_count": valid_count,
        "scope_start_date": str(first) if first is not None else None,
        "scope_end_date": str(last) if last is not None else None,
        "segments": segments,
        "total_segment_count": total_segments,
        "truncated": total_segments > len(segments),
        "max_segments": max_segments,
        "value_changes": value_changes,
        "interpretation": (
            "区段只表示所选已导入记录按原日期和单元格排序后的相邻状态；"
            "不证明日/月连续，不代表缺行，也不补造原表记录。"
        ),
    }
