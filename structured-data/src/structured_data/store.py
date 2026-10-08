"""Immutable dataset snapshots and a small, parameterized query surface."""
from __future__ import annotations

import csv
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time

import duckdb


def json_default(value):
    if isinstance(value, (dt.date, dt.datetime)):
        return value.isoformat()
    raise TypeError(type(value).__name__)


def dump(path: Path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2, default=json_default), encoding="utf-8")


class DataStore:
    def __init__(self, root: Path | str):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)
        self.datasets = self.root / "datasets"
        self.datasets.mkdir(exist_ok=True)

    def _path(self, dataset_id: str) -> Path:
        if not re.fullmatch(r"[0-9a-f]{24}", dataset_id):
            raise ValueError("数据集 ID 无效，请先调用 list_datasets。")
        path = self.datasets / dataset_id
        if not (path / "manifest.json").is_file():
            raise ValueError("数据集不存在或尚未导入完成。")
        return path

    def manifest(self, dataset_id: str) -> dict:
        return json.loads((self._path(dataset_id) / "manifest.json").read_text())

    def chart_index(self, dataset_id):
        from .native_charts import index
        return index(self, dataset_id)

    def catalog(self, dataset_id, sheet=None, question=''):
        from .native_charts import catalog
        return catalog(self, dataset_id, sheet, question)

    def list_charts(self, dataset_id, sheet=None, search='', limit=50, offset=0):
        self._limit(limit)
        if type(offset) is not int or offset < 0:
            raise ValueError('offset 必须为非负整数。')
        idx = self.chart_index(dataset_id)
        charts = [c for c in idx['charts'] if (sheet is None or c['sheet']==sheet)
                  and all(t.lower() in (c['title']+' '+c['sheet']).lower() for t in search.split())]
        return {'dataset_id':dataset_id, 'charts':charts[offset:offset+limit],
                'total':len(charts), 'offset':offset, 'truncated':len(charts)>offset+limit,
                'next_offset':offset+limit if len(charts)>offset+limit else None,
                'sheets':sorted({c['sheet'] for c in idx['charts']}), 'notice':idx['notice']}

    def list_datasets(self) -> list[dict]:
        items = []
        for path in sorted(self.datasets.glob("*/manifest.json")):
            doc = json.loads(path.read_text())
            items.append({k: doc[k] for k in ("dataset_id", "source_file", "sha256", "rule_version", "imported_at", "series_count", "observation_count", "valid_count", "issue_count", "sheets", "excluded_regions")})
        for item in items:
            coverage = self.manifest(item["dataset_id"]).get("coverage", [])
            item["pending_sheets"] = [s["name"] for s in coverage if s["status"] in {"needs_review", "unsupported", "empty"}]
            item["sheets"] = [{k:s[k] for k in ("name","observations","series_count","parsed_range") if k in s} for s in item["sheets"]]
            item["excluded_regions"] = [s for s in item["excluded_regions"] if s.get("range") != "entire_sheet"]
            if coverage:
                item["coverage_notice"] = "pending_sheets仅说明这些工作表尚未确认或不支持，不代表同名主题在已导入指标中不存在。未指定工作表时必须先用resolve_fields理解字段；明确指定未导入表时不可换表。已导入sheet也仅覆盖parsed_range。"
        items.sort(key=lambda d: d["imported_at"], reverse=True)
        seen = set()
        for item in items:
            key = item["source_file"]
            item["is_latest_for_source"] = key not in seen
            seen.add(key)
        return items

    def import_file(self, path: str | Path) -> dict:
        from .profiles.cotton import parse_workbook

        started = time.monotonic()
        parsed = parse_workbook(Path(path))
        result = self.publish(parsed)
        result["elapsed_seconds"] = round(time.monotonic() - started, 3)
        return result

    def publish(self, parsed: dict) -> dict:
        dataset_id = hashlib.sha256((parsed["sha256"] + ":" + parsed["rule_version"]).encode()).hexdigest()[:24]
        final = self.datasets / dataset_id
        if (final / "manifest.json").exists():
            return {"dataset_id": dataset_id, "reused": True, "manifest": self.manifest(dataset_id)}
        # Build out of view. A failed import cannot replace an existing valid snapshot.
        with tempfile.TemporaryDirectory(prefix=".import-", dir=self.datasets) as temp:
            work = Path(temp)
            observations = parsed["observations"]
            csv_path = work / "observations.csv"
            fields = ["series_id", "date", "value", "source_cell", "formula", "value_status", "date_cell", "raw_value", "period", "period_cell", "metric_cell"]
            with csv_path.open("w", newline="", encoding="utf-8") as stream:
                writer = csv.DictWriter(stream, fieldnames=fields, extrasaction="ignore")
                writer.writeheader()
                writer.writerows(observations)
            with duckdb.connect(str(work / "data.duckdb")) as con:
                con.execute("CREATE TABLE observations(series_id VARCHAR NOT NULL, date DATE, value DOUBLE, source_cell VARCHAR NOT NULL, formula VARCHAR, value_status VARCHAR NOT NULL, date_cell VARCHAR, raw_value VARCHAR, period VARCHAR, period_cell VARCHAR, metric_cell VARCHAR)")
                con.execute("""INSERT INTO observations SELECT * FROM read_csv(?, header=true, delim=',', quote='"', escape='"', nullstr='', columns={'series_id':'VARCHAR','date':'DATE','value':'DOUBLE','source_cell':'VARCHAR','formula':'VARCHAR','value_status':'VARCHAR','date_cell':'VARCHAR','raw_value':'VARCHAR','period':'VARCHAR','period_cell':'VARCHAR','metric_cell':'VARCHAR'})""", [str(csv_path)])
                duplicates = con.execute("SELECT series_id, coalesce(cast(date AS VARCHAR), period, source_cell), count(*) FROM observations GROUP BY 1,2 HAVING count(*)>1 LIMIT 5").fetchall()
                if duplicates:
                    raise ValueError(f"发现重复日期和指标，快照未发布：{duplicates}")
                con.execute("CREATE INDEX observation_lookup ON observations(series_id,date)")
                stats = con.execute("SELECT series_id,count(*),count(value),min(date),max(date),min(date) FILTER(WHERE value IS NOT NULL),max(date) FILTER(WHERE value IS NOT NULL) FROM observations GROUP BY series_id").fetchall()
            csv_path.unlink()
            summaries = {r[0]: {"record_count": r[1], "valid_count": r[2], "first_date": r[3], "last_date": r[4], "first_valid_date": r[5], "last_valid_date": r[6]} for r in stats}
            period_values={}
            for o in observations:
                if o.get('period'):period_values.setdefault(o['series_id'],{}).setdefault(o['period'],None)
            series = [{**s, **summaries.get(s["series_id"], {}), **({'periods':list(period_values[s['series_id']])} if s['series_id'] in period_values else {})} for s in parsed["series"]]
            dump(work / "series.json", series)
            dump(work / "issues.json", parsed.get("issues", []))
            manifest = {k: v for k, v in parsed.items() if k not in ("observations", "series", "issues")}
            source_path = Path(parsed["source_file"])
            if source_path.is_file():
                shutil.copyfile(source_path, work / "source.xlsx")
                with (work / "source.xlsx").open("rb") as original:
                    if hashlib.file_digest(original, "sha256").hexdigest() != parsed["sha256"]:
                        raise ValueError("读取期间原文件发生变化，未发布数据。")
            manifest["source_file"] = source_path.name
            manifest.update(dataset_id=dataset_id, imported_at=dt.datetime.now(dt.timezone.utc).isoformat(), series_count=len(series), observation_count=len(observations), valid_count=sum(s["valid_count"] for s in summaries.values()), issue_count=len(parsed.get("issues", [])))
            dump(work / "manifest.json", manifest)
            try:
                os.rename(work, final)
            except OSError:
                if (final / "manifest.json").exists():
                    return {"dataset_id": dataset_id, "reused": True, "manifest": self.manifest(dataset_id)}
                raise
        return {"dataset_id": dataset_id, "reused": False, "manifest": manifest}

    def list_series(self, dataset_id: str, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None) -> dict:
        self._limit(limit)
        series = self.catalog(dataset_id, sheet=sheet, question=search)
        if isinstance(offset, bool) or not isinstance(offset, int) or offset < 0:
            raise ValueError("offset 必须为非负整数。")
        from .catalog_search import normalize, aliases_for, search_text
        coverage = self.manifest(dataset_id).get("coverage", [])
        pending = [s for s in coverage if s["status"] in {"needs_review", "unsupported", "empty"}]
        if sheet is not None:
            series = [s for s in series if s['sheet']==sheet or s.get('chart_sheet')==sheet]
            pending = [s for s in pending if s['name']==sheet]
            if not series:
                return {"dataset_id":dataset_id,"series":[],"total":0,"offset":offset,"next_offset":None,
                        "truncated":False,"match_mode":"pending_scope" if pending else "sheet_not_found",
                        "pending_sheets":pending,"notice":"指定工作表没有已导入指标；不能替换为其他工作表。"}
        terms = normalize(search).split()
        aliases = {s['series_id']:aliases_for(s) for s in series}
        direct = [s for s in series if all(t in search_text(s) for t in terms)]
        matches = [s for s in series if all(t in search_text(s,aliases[s['series_id']]) for t in terms)]
        mode = "alias_candidates" if len(matches)>len(direct) else "all_terms"
        if terms and not matches:
            # A missing source/table must not turn into a different commodity scope.
            corpus = json.dumps(series, ensure_ascii=False).lower()
            missing_terms = [t for t in terms if len(t) >= 2 and t not in corpus]
            scored_pending = [(sum(t in s["name"].lower() for t in terms), s) for s in pending
                              if any(t in s["name"].lower() for t in missing_terms)
                              or s["name"].lower() in search.lower()]
            if scored_pending:
                best = max(score for score, _ in scored_pending)
                relevant = [{"name":s["name"], "status":s["status"], "reasons":s.get("reasons", [])}
                            for score, s in scored_pending if score == best]
                return {"dataset_id":dataset_id, "series":[], "total":0, "offset":offset,
                        "next_offset":None, "truncated":False, "match_mode":"pending_scope",
                        "pending_sheets":relevant[:20],
                        "notice":"查询涉及的工作表尚未确认映射，目前不能查询；这不代表原表缺数据。不可用其他工作表、国家或指标替代。请在结构化工作台核对并确认该表。"}
            scored = [(sum(t in json.dumps(s, ensure_ascii=False).lower() for t in terms), s) for s in series]
            best = max((score for score, _ in scored), default=0)
            if best:
                matches = [s for score, s in scored if score == best]
                mode = "partial_candidates"
        # Context ranks candidates; it never changes a caller's explicit scope.
        if context_sheet:
            matches.sort(key=lambda s:s['sheet']!=context_sheet)
        selected=[]
        for s in matches[offset:offset+limit]:
            item=dict(s)
            if mode=='alias_candidates' and not all(t in search_text(s) for t in terms):
                item['search_aliases']=aliases[s['series_id']]
                item['match_reason']='通过指标别名找到候选；须核对货币方向、原始口径和单位。'
            selected.append(item)
        notice = {'partial_candidates':'这些是部分关键词匹配的候选指标，不代表全部条件匹配。比较多个指标时按各指标分别选精确ID。',
                  'alias_candidates':'别名仅用于发现候选，不证明业务口径等价；按原指标、货币方向和来源核对。context_sheet仅优先排序，不自动认定候选。'}
        return {"dataset_id": dataset_id, "series": selected, "total": len(matches), "offset": offset, "next_offset": offset+limit if len(matches)>offset+limit else None, "truncated": len(matches) > offset+limit,
                "match_mode": mode, "notice": notice.get(mode,"已找到符合关键词的指标；按原指标名称、单位及用户条件选择ID查询数值。" if matches else "目录搜索无结果不等于工作簿没有数据，可使用单个关键词或留空查看目录。")}

    def convert_price(self, dataset_id, price_series_id, fx_series_id, date, target_unit='元/吨'):
        """Source-bound decimal conversion; never guess rate direction or units."""
        from decimal import Decimal, ROUND_HALF_UP
        from .catalog_search import currency_pair
        price=self.query_series(dataset_id,price_series_id,date,date)
        fx=self.query_series(dataset_id,fx_series_id,date,date)
        if not date or price['series'].get('date_mode','date') in ('none','period') or fx['series'].get('date_mode','date') in ('none','period'):
            raise ValueError('换算必须指定两项指标共有的具体日期')
        if currency_pair(fx['series'])!=('USD','CNY'):
            raise ValueError('当前只支持原指标明确为美元兑人民币的方向；不猜测或倒置汇率')
        weight={'磅':Decimal('0.45359237'),'公斤':Decimal('1'),'千克':Decimal('1'),'吨':Decimal('1000')}
        units={f'{currency}/{mass}':(factor,kg) for currency,factor in [('美元',Decimal('1')),('美分',Decimal('0.01'))] for mass,kg in weight.items()}
        source_unit=price['series'].get('unit')
        if source_unit not in units or target_unit not in {f'元/{mass}' for mass in weight}:
            raise ValueError('需要明确的美元或美分重量单价，目标单位支持元/磅、元/公斤、元/千克、元/吨')
        if price['record_count']!=1 or fx['record_count']!=1 or price['valid_count']!=1 or fx['valid_count']!=1:
            raise ValueError('指定日期的价格或汇率缺失/错误/不唯一，不能换算')
        p=Decimal(str(price['rows'][0]['value']));rate=Decimal(str(fx['rows'][0]['value']))
        if rate<=0:raise ValueError('汇率必须为正数')
        currency_factor,source_kg=units[source_unit];target_kg=weight[target_unit.split('/')[1]]
        value=p*currency_factor*rate*target_kg/source_kg
        return {'date':date,'value':float(value),'value_decimal':str(value),
                'rounded_value':str(value.quantize(Decimal('0.01'),rounding=ROUND_HALF_UP)),
                'unit':target_unit,'formula':f'{p} × {currency_factor} × {rate} × {target_kg} ÷ {source_kg}',
                'price':price,'exchange_rate':fx,
                'notice':'纯币种及重量单位换算，不含关税、增值税、运费及其他通关成本。汇率方向来自原指标名称；公式源数据使用Excel保存结果，未重算。'}

    def _series(self, dataset_id, series_id):
        if isinstance(series_id, str) and series_id.startswith('chart_'):
            idx=self.chart_index(dataset_id)
            for s in idx['views']+idx.get('curve_views',[]):
                if s['series_id']==series_id:
                    return s
            raise ValueError('图表指标不存在或引用尚未核验。')
        for s in json.loads((self._path(dataset_id) / "series.json").read_text()):
            if s["series_id"] == series_id:
                return s
        raise ValueError("指标不存在，请调用 list_series 取得精确 series_id，不能猜测。")

    @staticmethod
    def _limit(limit):
        if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 200:
            raise ValueError("limit 必须为 1 至 200。")

    @staticmethod
    def _date(value):
        if value is None or value == "":
            return None
        if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}", value):
            raise ValueError("日期必须为 YYYY-MM-DD。")
        return dt.date.fromisoformat(value)

    def _connect(self, dataset_id):
        return duckdb.connect(str(self._path(dataset_id) / "data.duckdb"), read_only=True, config={"enable_external_access": "false", "threads": "2", "memory_limit": "256MB"})

    def query_series(self, dataset_id: str, series_id: str, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None) -> dict:
        self._limit(limit)
        if operation not in {"rows", "latest", "mean", "sum", "min", "max", "count"}:
            raise ValueError("不支持的操作。")
        start, end = self._date(start_date), self._date(end_date)
        if start and end and start > end:
            raise ValueError("开始日期不能晚于结束日期。")
        series = self._series(dataset_id, series_id)
        mode=series.get('date_mode','date')
        if mode in ('none','period') and (start or end):raise ValueError('此指标没有具体日期，请使用期间/版本标签或不限定时间')
        if mode in ('none','period') and operation=='latest':raise ValueError('无具体日期的指标不推测最新版本，请指定期间标签或逐条查询')
        if period and mode!='period':raise ValueError('此指标不是期间矩阵/期间标签表，不能按期间过滤')
        if period is not None and (not isinstance(period,str) or len(period)>200):raise ValueError('期间标签无效')
        manifest = self.manifest(dataset_id)
        where = "series_id = ?"
        params = [series.get('source_series_id', series_id) if series.get('chart_id') else series_id]
        if series.get('chart_id'):
            from .native_charts import _range_sql
            clauses = []
            for ref in series['chart_ranges']:
                clause, values = _range_sql(ref)
                clauses.append('('+clause+')'); params.extend(values)
            where += ' AND ('+' OR '.join(clauses)+')'
        if start:
            where += " AND date >= ?"
            params.append(start)
        if end:
            where += " AND date <= ?"
            params.append(end)
        if period:
            where += " AND period = ?";params.append(period)
        if mode=='period' and not period and operation not in ('rows','count'):
            raise ValueError('不同年度/版本可能不可加总，请先指定期间标签再统计')
        with self._connect(dataset_id) as con:
            columns={r[1] for r in con.execute("PRAGMA table_info('observations')").fetchall()}
            extra=",period,period_cell,metric_cell" if 'period' in columns else ",NULL,NULL,NULL"
            count, valid, first, last = con.execute(f"SELECT count(*),count(value),min(date),max(date) FROM observations WHERE {where}", params).fetchone()
            aggregate = None
            extremum_count = None
            if operation not in {"rows", "latest"}:
                function = {"mean": "avg", "sum": "sum", "min": "min", "max": "max", "count": "count"}[operation]
                aggregate = con.execute(f"SELECT {function}(value) FROM observations WHERE {where}", params).fetchone()[0]
            if operation == "latest":
                rows = con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} AND value IS NOT NULL ORDER BY date DESC LIMIT 1", params).fetchall()
            elif operation in {"min", "max"}:
                # Return the actual extremum rows with their dates, never early
                # source examples that a caller could pair with the aggregate.
                extremum_count=con.execute(f"SELECT count(*) FROM observations WHERE {where} AND value=?",params+[aggregate]).fetchone()[0]
                rows=con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} AND value=? ORDER BY date NULLS LAST,source_cell LIMIT ?",params+[aggregate,limit]).fetchall()
            else:
                rows = con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} ORDER BY date NULLS LAST,source_cell LIMIT ?", params + [limit if operation == "rows" else 10]).fetchall()
        records = [{"date": str(r[0]) if r[0] else None, "period":r[7],"period_cell":r[8],"metric_cell":r[9], "value": r[1], "source_cell": r[2], "value_status": r[3], "formula": r[4], "date_cell": r[5], "raw_value": r[6]} for r in rows]
        for record in records:
            record['category_cell']=series.get('category_provenance',{}).get(record['source_cell'])
        warnings = []
        if series.get('chart_id'):
            warnings.append('图表：'+series['chart_sheet']+' / '+series['metric']+'；只查询已关联曲线范围，使用源表日期及保存数值，未用图表缓存补值。')
            if series.get('chart_excluded_curves'):
                warnings.append('尚未纳入查询的曲线：'+'；'.join(c['label']+'（'+c['reason']+'）' for c in series['chart_excluded_curves']))
        if mode=='period':warnings.append('期间和版本保留原表标签；不得把年度、预测月份或情景解释为具体日期。')
        if mode=='none':warnings.append('原表没有时间字段，不能据此判断数据日期或最新时间。')
        if series.get('date_mode')=='shared_cell':
            warnings.append('此系列使用小表共用日期，类别见dimensions；总计与各分类是不同系列，不得重复加总。')
        if not series.get("unit"):
            warnings.append("原表未明确单位，不应自行补充或换算单位。")
        if count > valid:
            warnings.append(f"范围内 {count-valid} 条记录没有有效数值；统计排除这些记录。")
        if not valid:
            warnings.append("查询范围没有有效数据，不能用其他日期或模型记忆补齐。")
        if operation == "sum":
            warnings.append("结果是观测值的算术加总；库存、价格等指标是否适合加总需结合业务含义。")
        if operation in {'min','max'}:
            warnings.append('极值日期只能使用extremum_rows中的date；actual_first_date/actual_last_date是查询范围边界，不是极值日期。并列极值均保留，超出limit时标记截断。')
        warnings.append("公式结果来自原文件保存的缓存，本服务未重新计算 Excel 公式。")
        if series["sheet"] == "郑棉仓单":
            warnings.append("源工作簿包含 Wind ShowBlank==0 配置，已有零值可能含上游缺失填充，无法逆向区分。")
        warnings.append("missing_count统计已导入记录中的空白、错误或无缓存值；不补建原表没有的记录。")
        filters = {"series_id": series_id, "start_date": start_date, "end_date": end_date, "operation": operation, "period":period}
        return {"dataset_id": dataset_id, "status": "ok" if valid else "no_data", "series": series, "filters": filters, "record_count": count, "valid_count": valid, "missing_count": count-valid, "actual_first_date": str(first) if first else None, "actual_last_date": str(last) if last else None, "aggregate": aggregate, "rows": records, "extremum_rows":records if operation in {'min','max'} else None, "extremum_count":extremum_count, "rows_are": "extremum_observations" if operation in {'min','max'} else "latest_valid_observation" if operation == "latest" else "observations" if operation == "rows" else "source_examples_not_all_inputs", "truncated": (extremum_count>len(records)) if extremum_count is not None else operation != "latest" and count > len(records), "source": {"file": manifest["source_file"], "sha256": manifest["sha256"], "sheet": series["sheet"], "header_cells": series.get("header_cells", [])}, "warnings": warnings}

    def compare_series(self, dataset_id: str, left_series_id: str, right_series_id: str, date: str) -> dict:
        if not self._date(date):
            raise ValueError("必须提供比较日期。")
        left = self.query_series(dataset_id, left_series_id, date, date)
        right = self.query_series(dataset_id, right_series_id, date, date)
        if left["status"] != "ok" or right["status"] != "ok":
            return {"status": "no_data", "difference": None, "left": left, "right": right}
        lu, ru = left["series"].get("unit"), right["series"].get("unit")
        ls, rs = left["series"], right["series"]
        raw_price_pair = (lu is None and ru is None and ls["sheet"] == rs["sheet"] == "郑棉期货"
                          and ls.get("dimensions", {}).get("contract") == rs.get("dimensions", {}).get("contract")
                          and {ls.get("dimensions", {}).get("block"), rs.get("dimensions", {}).get("block")} == {"close", "settle"})
        if not raw_price_pair and (not lu or not ru or lu != ru):
            raise ValueError("两个指标单位不明确或不一致，不能自动计算差值。")
        return {"status": "ok", "definition": "left_minus_right", "difference": left["rows"][0]["value"]-right["rows"][0]["value"], "unit": lu,
                "comparison_basis": "same_contract_raw_price_values_unit_unconfirmed" if raw_price_pair else "same_explicit_unit",
                "warnings": ["仅比较原表同一合约收盘/结算的原始数值；单位尚未确认，未做单位换算，不得补造单位。"] if raw_price_pair else [],
                "left": left, "right": right}
