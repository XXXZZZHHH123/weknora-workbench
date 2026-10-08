"""Independent source-checked acceptance cases; opt in with COTTON_ACCEPTANCE=1.

Expected values live in acceptance_cases.json and were checked directly against
the original workbook before the parser or product database was inspected.
This checks deterministic tools, not an LLM conversation.
"""
from __future__ import annotations

import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path

import pytest

from structured_data.store import DataStore

BASE = Path(__file__).resolve().parents[1]
SPEC_PATH = BASE / "tests/acceptance_cases.json"
if not SPEC_PATH.exists():
    pytest.skip("Private acceptance fixtures are not included in the source repository", allow_module_level=True)
SPEC = json.loads(SPEC_PATH.read_text())
DATASET_ID = os.environ.get("COTTON_ACCEPTANCE_DATASET", "23f7203270045cf84d7ae978")


def execute(case, store):
    sid, date, operation = case["series_id"], case["date"], case["operation"]
    if operation == "difference":
        try:
            return store.compare_series(DATASET_ID, sid[0], sid[1], date)
        except ValueError as exc:
            return {"status": "rejected", "error": str(exc)}
    if operation == "unsupported_region":
        return {"catalog": store.list_series(DATASET_ID, search=case["series_selector"]["metric"], limit=200),
                "excluded_regions": store.manifest(DATASET_ID)["excluded_regions"]}
    if operation == "statistics":
        return {op: store.query_series(DATASET_ID, sid, date["start"], date["end"], operation=op)
                for op in ("mean", "min", "max", "sum", "count")}
    return store.query_series(DATASET_ID, sid, date, date, operation="latest" if operation == "latest" else "rows")


def check(case, actual):
    exp, operation, errors = case["expected"], case["operation"], []
    def require(condition, detail):
        if not condition:
            errors.append(detail)
    def close(got, want):
        return isinstance(got, (int, float)) and math.isclose(got, want, rel_tol=0, abs_tol=exp.get("abs_tolerance", 1e-9))
    def source(result, expected_cell, expected_date_cell):
        require(result["source"]["file"] == case["source"]["file"], "来源文件不一致")
        require(result["source"]["sheet"] == case["source"]["sheet"], "来源工作表不一致")
        require(result["source"]["sha256"] == SPEC["source_sha256"], "来源指纹不一致")
        if result.get("rows"):
            require(result["rows"][0]["source_cell"] == expected_cell, "来源数值单元格不一致")
            require(result["rows"][0]["date_cell"] == expected_date_cell, "来源日期单元格不一致")

    if operation == "unsupported_region":
        require(actual["catalog"]["total"] == 0, "排除区块指标不应导入")
        require(any(r["sheet"] == case["source"]["sheet"] and r.get("reason") for r in actual["excluded_regions"]), "未说明排除范围")
    elif exp.get("status") == "incompatible_or_unconfirmed_units":
        require(actual["status"] == "rejected" and "单位" in actual.get("error", ""), "未拒绝跨单位/单位未明确的差值")
    elif operation == "difference":
        require(actual.get("status") == "ok", "差值未成功")
        require(close(actual.get("difference"), exp["value"]), "差值数值不一致")
        require(actual.get("unit") == exp.get("unit"), "差值单位不一致")
        require(actual.get("definition") == "left_minus_right", "未说明差值方向")
        if actual.get("left"):
            for i, side in enumerate(("left", "right")):
                require(close(actual[side]["rows"][0]["value"], exp[side]), side + "操作数不一致")
                source(actual[side], case["source"]["value_cells"][i], case["source"]["date_cells"][min(i, len(case["source"]["date_cells"])-1)])
        if exp.get("unit_status") == "needs_review":
            require(actual.get("comparison_basis") == "same_contract_raw_price_values_unit_unconfirmed" and bool(actual.get("warnings")), "未声明仅原始数值差且单位未确认")
    elif operation == "statistics":
        for op in ("mean", "min", "max", "sum", "count"):
            require(close(actual[op]["aggregate"], exp[op]), op + "计算不一致")
            require(actual[op]["valid_count"] == exp["count"], op + "样本数不一致")
            require([r["source_cell"] for r in actual[op]["rows"]] == case["source"]["value_cells"], op + "来源不一致")
            require([r["value"] for r in actual[op]["rows"]] == exp["values"], op + "参与值不一致")
    elif exp.get("value") is None:
        require(actual["status"] == "no_data", "应明确返回无有效数据")
        require(all(r["value"] is None for r in actual["rows"]), "缺失被替换成了数值")
        if exp["status"] in ("excel_error", "unexpected_text", "blank"):
            require(bool(actual["rows"]), "没有返回原始缺失/异常记录，无法核验具体空白来源")
            if actual["rows"]:
                row = actual["rows"][0]
                require(row["value_status"] == exp["status"], "缺失/异常类别不一致")
                require(row["raw_value"] == exp.get("raw_value"), "原始异常值不一致")
                source(actual, case["source"]["value_cells"][0], case["source"]["date_cells"][0])
        else:
            require(not actual["rows"], "范围外日期不应返回其他日期的记录")
            require(actual["series"]["last_valid_date"] == exp["latest_valid_date"], "最新有效日期不一致")
    else:
        require(actual["status"] == "ok" and len(actual["rows"]) == 1, "应返回唯一有效记录")
        if actual["rows"]:
            row = actual["rows"][0]
            require(close(row["value"], exp["value"]), "点值不一致")
            require(row["date"] == exp.get("date", case["date"]), "日期不一致")
            require(actual["series"].get("unit") == exp.get("unit"), "单位不一致或擅自补充")
            source(actual, case["source"]["value_cells"][0], case["source"]["date_cells"][0])
            if "formula" in exp:
                require(row["formula"] == exp["formula"], "公式来源不一致")
    return errors


@pytest.mark.skipif(os.environ.get("COTTON_ACCEPTANCE") != "1", reason="真实来源验收需显式 COTTON_ACCEPTANCE=1")
@pytest.mark.parametrize("case", SPEC["cases"], ids=lambda c: c["id"])
def test_source_checked_case(case):
    actual = execute(case, DataStore(BASE / "data"))
    assert not (errors := check(case, actual)), "; ".join(errors)


def run_report():
    source_path = Path(SPEC["source_path"])
    before = hashlib.sha256(source_path.read_bytes()).hexdigest()
    store = DataStore(BASE / "data")
    results = []
    for case in SPEC["cases"]:
        try:
            actual = execute(case, store)
            errors = check(case, actual)
        except Exception as exc:
            actual, errors = {"error": str(exc)}, ["执行异常：" + str(exc)]
        results.append({"id": case["id"], "question": case["question"], "expected": case["expected"], "actual": actual, "passed": not errors, "gaps": errors})
    after = hashlib.sha256(source_path.read_bytes()).hexdigest()
    report = {"tested_at": dt.datetime.now(dt.timezone.utc).isoformat(), "level": "deterministic_store_tools_only_not_llm_end_to_end", "dataset_id": DATASET_ID, "source_sha256_before": before, "source_sha256_after": after, "source_unchanged": before == after == SPEC["source_sha256"], "passed": sum(r["passed"] for r in results), "total": len(results), "results": results}
    (BASE / "results/tool-acceptance.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    lines = ["# 棉花结构化数据工具级验收记录", "", "## 当前状态", "", f"工具层测试 {report['passed']}/{report['total']} 通过。直接调用已导入真实数据的查询工具；本报告未验证 LLM 选工具、选指标和最终回答，不能据此声称端到端准确率。真实问答见同目录的独立 LLM 测试报告。", "", "预期值由本轮独立重读原始 Excel 得到，未按解析器结果修改。", "", "修复历史：v1 为 15/16 通过，SD-08 因未保留普通空白单元格，不能给出 B3342/A3342 来源而未通过。该证据保留在 tool-acceptance-v1-before-blank-fix.json 和 验收记录-v1修复前.md。v2 保留空白观测后重新执行全部用例，业务预期未改变。", "", f"本轮数据集：`{DATASET_ID}`。测试前后原文件 SHA-256 均为 `{before}`；原件未改动：{report['source_unchanged']}。", "", "完整接口返回值见同目录 tool-acceptance.json。", ""]
    expected_text = {
        "SD-01": "17110，来源 B4705；单位未确认。",
        "SD-02": "17200－17110＝90，结算减收盘，来源 K4705、B4705；仅作原始数值比较，不补造单位。",
        "SD-03": "16720，来源 P4705；不能误取 CF11M 的 Q4705=17375。",
        "SD-04": "不能给出有效成交量。AC4705 是 Excel 错误值 #N/A，不能当作 0。",
        "SD-05": "明确无此日期数据；该指标最新有效日期为 2023-09-22，不能拿最新值替代。",
        "SD-06": "5 个观测值：17195、17370、17165、17110、17110；均值 17190，最小 17110，最大 17370，加总 85950。",
        "SD-07": "最新有效值 5160，日期 2023-09-21，来源 B3340；9 月 22、25 日该列为空。",
        "SD-08": "9 月 25 日是表内空白：B3342 为空，A3342 为该日期。说明缺失并给出来源，不能当作 0 或替换日期。",
        "SD-09": "20.784 万吨，来源 AF3340；这是原工作簿保存的公式结果。",
        "SD-10": "内地库 12.664 减新疆库 8.12，等于 4.544 万吨；来源 AH3340、AG3340。",
        "SD-11": "110，来源 C87；原表未明确单位，应说明待确认。",
        "SD-12": "298.93291576470585，来源 G87；保留其公式来源，不声称重新计算了 Excel。",
        "SD-13": "不能给出有效数值。V13 混入重复标题“纱厂月度负荷”，应识别为文本异常。",
        "SD-14": "明确无此日期数据；该指标最新有效日期为 2023-10-31，不能补造 2024 年数据。",
        "SD-15": "说明商业合计属于首期未导入的并排区域。原表 AH4=106.3 对应 AG4 的 2017-09-30，不能错绑定 A4 的 2016-11-30。",
        "SD-16": "拒绝直接相减：B 列单位未确认，AF 列为万吨。",
    }
    for case, result in zip(SPEC["cases"], results):
        actual = result["actual"]
        if "left" in actual:
            operands = [actual[s]["rows"][0] for s in ("left", "right")]
            summary = f"返回 {operands[0]['value']}－{operands[1]['value']}＝{actual['difference']}；单位：{actual.get('unit') or '未确认'}。来源 {operands[0]['source_cell']}、{operands[1]['source_cell']}。" + " ".join(actual.get("warnings", []))
        elif case["operation"] == "statistics":
            summary = f"返回均值 {actual['mean']['aggregate']}，最小值 {actual['min']['aggregate']}，最大值 {actual['max']['aggregate']}，加总 {actual['sum']['aggregate']}，有效记录 {actual['count']['aggregate']} 条。来源 B4701:B4705。"
        elif "rows" in actual:
            if not actual["rows"]:
                summary = "返回无有效数据（no_data），未返回任何数值或来源行。指标最新有效日期：" + str(actual["series"].get("last_valid_date")) + "。"
            else:
                row = actual["rows"][0]
                summary = f"日期 {row['date']}，数值 {row['value'] if row['value'] is not None else '无有效数值'}，单位 {actual['series'].get('unit') or '未确认'}；来源 {row['source_cell']}，日期来源 {row['date_cell']}。"
                if row["value"] is None:
                    summary += f" 异常类型 {row['value_status']}，原始内容 {row['raw_value']}。"
                if row.get("formula"):
                    summary += f" 原公式 {row['formula']}，使用保存的缓存结果。"
        elif "catalog" in actual:
            summary = f"指标目录匹配到 {actual['catalog']['total']} 个指标；数据集说明包含该工作表排除区域：" + "；".join(r["range"] + "，" + r["reason"] for r in actual["excluded_regions"])
        else:
            summary = "查询被拒绝：" + actual.get("error", json.dumps(actual, ensure_ascii=False))
        gap = "；".join(result["gaps"]) if result["gaps"] else "本题工具返回符合独立预期。模型最终回答尚未测试。"
        if case["id"] == "SD-08" and result["gaps"]:
            gap += "。已做到不编造数值；尚不能区分表内空白和表外日期，缺少原单元格来源，属于部分满足。"
        lines += ["## " + case["id"], "", "**问题是什么：** " + case["question"], "", "**预期答案是什么：** " + expected_text[case["id"]], "", "**返回的实际结果是什么：** " + summary, "", "**和预期答案的差距是什么：** " + gap, ""]
    (BASE / "results/验收记录.md").write_text("\n".join(lines))
    print(json.dumps({k: report[k] for k in ("passed", "total", "source_unchanged")}, ensure_ascii=False))
    for result in results:
        if result["gaps"]:
            print(result["id"], "; ".join(result["gaps"]))


if __name__ == "__main__":
    run_report()
