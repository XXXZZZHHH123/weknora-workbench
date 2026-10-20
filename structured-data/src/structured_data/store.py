"""Immutable dataset snapshots and a small, parameterized query surface."""
from __future__ import annotations

import csv
from contextlib import closing, contextmanager
import datetime as dt
import errno
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time

import duckdb


@contextmanager
def database_lock(path, *, exclusive=False, blocking=True):
    # Keep the lock file: removing it can give waiting processes different
    # inodes and allow recovery to race an already open read-only connection.
    with Path(str(path) + ".lock").open("a+b") as stream:
        if os.name == "nt":
            import msvcrt
            stream.seek(0)
            if not stream.read(1):
                stream.write(b"\0")
                stream.flush()
            stream.seek(0)
            try:
                msvcrt.locking(stream.fileno(), msvcrt.LK_LOCK if blocking else msvcrt.LK_NBLCK, 1)
            except OSError as exc:
                if not blocking and exc.errno in {errno.EACCES, errno.EAGAIN}:
                    yield False
                    return
                raise
            try:
                yield True
            finally:
                stream.seek(0)
                msvcrt.locking(stream.fileno(), msvcrt.LK_UNLCK, 1)
        else:
            import fcntl
            operation = fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH
            try:
                fcntl.flock(stream.fileno(), operation | (0 if blocking else fcntl.LOCK_NB))
            except BlockingIOError:
                yield False
                return
            try:
                yield True
            finally:
                fcntl.flock(stream.fileno(), fcntl.LOCK_UN)


def json_default(value):
    if isinstance(value, (dt.date, dt.datetime)):
        return value.isoformat()
    raise TypeError(type(value).__name__)


def dump(path: Path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2, default=json_default), encoding="utf-8")


def _unimported_sheet_states(coverage, imported_sheets):
    """Coverage sheets present in the original but absent from this version.

    Imported sheet names and coverage states come from the saved manifest; no
    question text participates. pending/empty sheets keep their own fields.
    """
    imported = {sheet.get("name") for sheet in imported_sheets if isinstance(sheet, dict)}
    return [{"name": s["name"], "status": s["status"], "reasons": s.get("reasons", []),
             "imported_in_this_version": False}
            for s in coverage
            if s.get("name") not in imported and s.get("status") not in {"needs_review", "unsupported", "empty"}]


class DataStore:
    def __init__(self, root: Path | str):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)
        self.datasets = self.root / "datasets"
        self.datasets.mkdir(exist_ok=True)

    def select_dataset(self, dataset_id=None):
        """One explicit snapshot, or the only current source; never guess a file."""
        if dataset_id:
            self._path(dataset_id)
            return dataset_id
        latest=[d for d in self.list_datasets() if d['is_latest_for_source']]
        if not latest:
            raise ValueError('尚无已导入的数据集，请先导入文件')
        if len(latest)!=1:
            raise ValueError('有多份当前文件，请先list_datasets按用户所问文件选择真实dataset_id')
        return latest[0]['dataset_id']

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
        from .source_context import enrich_catalog
        return enrich_catalog(self, dataset_id, catalog(self, dataset_id, sheet, question))

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

    def list_datasets(self, *, include_samples=False) -> list[dict]:
        from .dataset_kind import dataset_kind
        items = []
        for path in sorted(self.datasets.glob("*/manifest.json")):
            if not re.fullmatch(r"[0-9a-f]{24}", path.parent.name):
                continue
            doc = json.loads(path.read_text())
            kind = dataset_kind(doc, self.root)
            if kind == 'sample' and not include_samples:
                continue
            items.append({'dataset_kind': kind, 'changed_sheets': doc.get('changed_sheets', []),
                          'import_id': doc.get('import_id'),
                          'use_reviewed_mappings': doc.get('use_reviewed_mappings', True),
                          'parent_dataset_id': doc.get('parent_dataset_id'),
                          **{k: doc[k] for k in ("dataset_id", "source_file", "sha256", "rule_version", "imported_at", "series_count", "observation_count", "valid_count", "issue_count", "sheets", "excluded_regions")}})
        for item in items:
            coverage = self.manifest(item["dataset_id"]).get("coverage", [])
            item["pending_sheets"] = [s["name"] for s in coverage if s["status"] in {"needs_review", "unsupported"}]
            item["pending_sheet_states"] = [
                {"name": s["name"], "status": s["status"], "reasons": s.get("reasons", [])}
                for s in coverage if s["status"] in {"needs_review", "unsupported"}
            ]
            item["empty_sheets"] = [s["name"] for s in coverage if s["status"] == "empty"]
            unimported = _unimported_sheet_states(coverage, item["sheets"])
            if unimported:
                item["unimported_sheet_states"] = unimported
            item["sheets"] = [{k:s[k] for k in ("name","observations","series_count","parsed_range") if k in s} for s in item["sheets"]]
            item["excluded_regions"] = [s for s in item["excluded_regions"] if s.get("range") != "entire_sheet"]
            if coverage:
                item["coverage_notice"] = "pending_sheets仅列工作表名称，不表示统一原因或都需要映射；请按同一数据集下pending_sheet_states的实际status和reasons分别判断，needs_review与unsupported不可混为一谈。unsupported不能表述为完成映射后即可使用。pending状态不证明原件无数据或整张表均未导入；同名工作表须按数据集身份分别判断。未指定工作表时核验真实目录字段，可按需用resolve_fields语义识别；明确指定未导入表时不可换表。已导入sheet也仅覆盖parsed_range。status、reasons和数据集身份供工具判断范围，最终回复无需复述内部键或ID。"
        items.sort(key=lambda d: d["imported_at"], reverse=True)
        source_keys, reviews = {}, {}
        seen = set()
        for item in items:
            keys = self._history_source_keys(item, reviews)
            source_keys[item['dataset_id']] = keys
            # One immutable snapshot may serve several identical native files.
            # A later version of one original must not hide the others.
            item["is_latest_for_source"] = bool(set(keys) - seen)
            seen.update(keys)
        from .dataset_history import describe_history
        return describe_history(items, source_keys=source_keys)

    def _history_source_keys(self, item, reviews):
        """Use persisted verified native IDs only inside their own KB directory.

        The authenticated import path already verified the original bytes and
        recorded the binding. A matching immutable manifest/review pair lets us
        distinguish same-name originals without rehashing large Excel files on
        every catalogue request. This is version grouping, never an ACL grant.
        """
        from .workspaces import current_identity, valid_knowledge_base_id
        legacy = (('filename', item['source_file']),)
        kb_id = self.root.name
        if self.root.parent.name != 'knowledge-bases' or not valid_knowledge_base_id(kb_id):
            return legacy
        identity = current_identity.get()
        if identity is not None and identity.knowledge_base_id != kb_id:
            return legacy
        import_id = item.get('import_id')
        if not isinstance(import_id, str) or not re.fullmatch(r'[0-9a-f]{24}', import_id):
            return legacy
        if import_id not in reviews:
            folder = self.root / 'imports' / import_id
            path = folder / 'review.json'
            try:
                if (folder.parent.is_symlink() or folder.is_symlink() or path.is_symlink()
                        or not path.resolve().is_relative_to(self.root.resolve())):
                    reviews[import_id] = None
                else:
                    reviews[import_id] = json.loads(path.read_text())
            except (OSError, ValueError):
                reviews[import_id] = None
        review = reviews[import_id]
        if (not isinstance(review, dict) or review.get('import_id') != import_id
                or review.get('sha256') != item['sha256']
                or review.get('source_file') != item['source_file']
                or not isinstance(review.get('native_source_bindings'), list)):
            return legacy
        keys = set()
        for binding in review['native_source_bindings']:
            if (not isinstance(binding, dict) or binding.get('knowledge_base_id') != kb_id
                    or not valid_knowledge_base_id(binding.get('source_knowledge_id'))
                    or type(binding.get('tenant_id')) is not int or binding['tenant_id'] <= 0
                    or (identity is not None and binding['tenant_id'] != identity.knowledge_base_tenant_id)
                    or not isinstance(binding.get('file_name'), str) or not binding['file_name']
                    or Path(binding['file_name'].replace('\\', '/')).name != binding['file_name']
                    or any(ord(char) < 32 for char in binding['file_name'])
                    or type(binding.get('file_size')) is not int or binding['file_size'] <= 0
                    or not isinstance(binding.get('file_hash'), str)
                    or not re.fullmatch(r'[0-9a-fA-F]{32}', binding['file_hash'])):
                continue
            keys.add(('native', binding['tenant_id'], kb_id, binding['source_knowledge_id']))
        return tuple(sorted(keys)) if keys else legacy

    def import_file(self, path: str | Path) -> dict:
        from .profiles.reviewed import parse_workbook

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
            with closing(duckdb.connect(str(work / "data.duckdb"))) as con:
                con.execute("CREATE TABLE observations(series_id VARCHAR NOT NULL, date DATE, value DOUBLE, source_cell VARCHAR NOT NULL, formula VARCHAR, value_status VARCHAR NOT NULL, date_cell VARCHAR, raw_value VARCHAR, period VARCHAR, period_cell VARCHAR, metric_cell VARCHAR)")
                con.execute("""INSERT INTO observations SELECT * FROM read_csv(?, header=true, delim=',', quote='"', escape='"', nullstr='', columns={'series_id':'VARCHAR','date':'DATE','value':'DOUBLE','source_cell':'VARCHAR','formula':'VARCHAR','value_status':'VARCHAR','date_cell':'VARCHAR','raw_value':'VARCHAR','period':'VARCHAR','period_cell':'VARCHAR','metric_cell':'VARCHAR'})""", [str(csv_path)])
                duplicates = con.execute("SELECT series_id, coalesce(cast(date AS VARCHAR), period, source_cell), count(*) FROM observations GROUP BY 1,2 HAVING count(*)>1 LIMIT 5").fetchall()
                if duplicates:
                    raise ValueError(f"发现重复日期和指标，快照未发布：{duplicates}")
                con.execute("CREATE INDEX observation_lookup ON observations(series_id,date)")
                stats = con.execute("SELECT series_id,count(*),count(value),min(date),max(date),min(date) FILTER(WHERE value IS NOT NULL),max(date) FILTER(WHERE value IS NOT NULL) FROM observations GROUP BY series_id").fetchall()
                con.execute("CHECKPOINT")
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
                manifest = self.manifest(dataset_id)
                unimported = [state for state in _unimported_sheet_states(coverage, manifest.get("sheets", [])) if state["name"] == sheet]
                if unimported and not pending:
                    return {"dataset_id":dataset_id,"series":[],"total":0,"offset":offset,"next_offset":None,
                            "truncated":False,"match_mode":"unimported_sheet","pending_sheets":[],"unimported_sheet_states":unimported,
                            "notice":"原件包含该工作表，但本数据集版本没有导入其字段；这不表示原件无数据。可用inspect_source指定该sheet定向读取原单元格、表头与单位作为来源证据，不能替换为其他工作表的同名指标。"}
                return {"dataset_id":dataset_id,"series":[],"total":0,"offset":offset,"next_offset":None,
                        "truncated":False,"match_mode":"pending_scope" if pending else "sheet_not_found",
                        "pending_sheets":pending,"notice":"指定工作表没有已导入指标；不能替换为其他工作表。"
                        + ("可用inspect_source指定该sheet读取原单元格作为来源证据。" if pending else "")}
        terms = normalize(search).split()
        aliases = {s['series_id']:aliases_for(s) for s in series}
        direct = [s for s in series if all(t in search_text(s) for t in terms)]
        matches = [s for s in series if all(t in search_text(s,aliases[s['series_id']]) for t in terms)]
        mode = "alias_candidates" if len(matches)>len(direct) else "all_terms"
        if terms and not matches:
            # A missing source/table must not turn into a different commodity scope.
            corpus = json.dumps(series, ensure_ascii=False).lower()
            missing_terms = [t for t in terms if len(t) >= 2 and t not in corpus]
            # Literal sheet-name hits only recall candidates. Sheets present in
            # the original but not imported in this version are candidates too.
            unimported_pool = _unimported_sheet_states(coverage, self.manifest(dataset_id).get("sheets", []))
            scored_pending = [(sum(t in s["name"].lower() for t in terms), s) for s in pending + unimported_pool
                              if any(t in s["name"].lower() for t in missing_terms)
                              or s["name"].lower() in search.lower()]
            if scored_pending:
                best = max(score for score, _ in scored_pending)
                relevant = [{"name":s["name"], "status":s["status"], "reasons":s.get("reasons", []),
                             **({"imported_in_this_version": False} if "imported_in_this_version" in s else {})}
                            for score, s in scored_pending if score == best]
                return {"dataset_id":dataset_id, "series":[], "total":0, "offset":offset,
                        "next_offset":None, "truncated":False, "match_mode":"pending_scope",
                        "pending_sheets":relevant[:20],
                        "notice":"查询涉及的工作表在本版本字段目录中尚未导入或尚未确认映射，不能用字段查询；这不代表原表缺数据。可用inspect_source指定该sheet定向读取原单元格、表头与单位作为来源证据；不可用其他工作表、国家或指标替代。如需长期按字段查询，在结构化工作台核对并导入该表。"}
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
            # A returned catalog row proves this source series was imported.
            # Keep that fact separate from metadata review such as an unknown unit.
            item['sheet_status'] = 'has_imported_series'
            if mode=='alias_candidates' and not all(t in search_text(s) for t in terms):
                item['search_aliases']=aliases[s['series_id']]
                item['match_reason']='通过指标别名找到候选；须核对货币方向、原始口径和单位。'
            selected.append(item)
        notice = {'partial_candidates':'这些是部分关键词匹配的候选指标，仅作召回证据，不代表全部条件匹配或已确定业务口径。',
                  'alias_candidates':'别名仅用于发现候选，不证明业务口径等价；按原指标、货币方向和来源核对。context_sheet仅优先排序，不自动认定候选。'}
        return {"dataset_id": dataset_id, "series": selected, "total": len(matches), "offset": offset, "next_offset": offset+limit if len(matches)>offset+limit else None, "truncated": len(matches) > offset+limit,
                "catalog_summary": self._catalog_summary(matches, sheet, search, mode),
                "match_mode": mode, "notice": notice.get(mode,"已返回当前搜索与工作表范围内的字段目录。" if matches else "目录搜索无结果不等于工作簿没有数据，可使用单个关键词或留空查看目录。") +
                "total为分页前匹配字段数，truncated与next_offset说明字段分页；catalog_summary另列实际分组及其展示覆盖。这是目录事实，不是业务观测值。sheet_status=has_imported_series表示该目录项已有已导入记录；unit_status=needs_review表示字段单位元数据尚待核对，不表示原件其他位置没有单位证据，也不表示工作表待导入。pending_sheets另行描述未确认或不支持的工作表。模型依据完整问题、上下文和来源决定是否需要读取记录或计算，不因取得目录而必须继续取数。"}

    @staticmethod
    def _catalog_summary(matches, sheet, search, mode):
        # Literal, deterministic catalogue facts. No domain classification or
        # choice of which groups answer the question happens in this layer.
        groups = {}
        for item in matches:
            key = (item['sheet'], item['metric'])
            groups.setdefault(key, set()).add(item['series_id'])
        ordered = sorted(groups.items())
        return {
            "kind": "catalog_group_counts",
            "scope": {"sheet": sheet, "search": search, "match_mode": mode},
            "series_count": len({item['series_id'] for item in matches}),
            "group_by": ["sheet", "metric"],
            "group_count": len(groups),
            "groups": [{"sheet": key[0], "metric": key[1], "series_count": len(ids)}
                       for key, ids in ordered[:100]],
            "groups_truncated": len(groups) > 100,
            "notice": "按本次全部匹配目录（分页前）的工作表和原指标名精确分组计数，可作为实际字段结构列举的来源。仅计数目录，不判断哪些指标属于用户所问的业务类别，不代表观测值统计。部分匹配或别名候选不能当作全部业务范围。",
        }

    def source_overlap(self, dataset_id, left_id, right_id):
        """Whole-history date/value agreement, supplementary identity evidence.

        Agreement never selects a business definition or overrides units.
        Parameterized joins use only verified IDs from this dataset.
        """
        self._series(dataset_id,left_id);self._series(dataset_id,right_id)
        with self._connect(dataset_id) as con:
            count,equal,first,last=con.execute("SELECT count(*),count(*) FILTER(WHERE a.value=b.value),min(a.date),max(a.date) FROM observations a JOIN observations b ON a.date=b.date WHERE a.series_id=? AND b.series_id=? AND a.value IS NOT NULL AND b.value IS NOT NULL",[left_id,right_id]).fetchone()
        return {'common_valid_dates':count,'exactly_equal_values':equal,'first_date':str(first) if first else None,'last_date':str(last) if last else None,'use':'辅助比较来源；数值相同不自动授权选择或合并不同业务定义'}

    def convert_price(self, dataset_id, price_series_id, fx_series_id, date, target_unit='元/吨', fx_operation='multiply'):
        """Source-bound decimal conversion; never guess rate direction or units."""
        from decimal import Decimal, ROUND_HALF_UP
        from .catalog_search import currency_pair
        price=self.query_series(dataset_id,price_series_id,date,date)
        fx=self.query_series(dataset_id,fx_series_id,date,date)
        if not date or price['series'].get('date_mode','date') in ('none','period') or fx['series'].get('date_mode','date') in ('none','period'):
            raise ValueError('换算必须指定两项指标共有的具体日期')
        direction = currency_pair(fx['series'])
        if (fx_operation, direction) not in {('multiply', ('USD', 'CNY')), ('divide', ('CNY', 'USD'))}:
            raise ValueError('汇率方向必须来自已核对的 base_currency/quote_currency 及原表来源；计划的乘除方向必须与其一致')
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
        applied_rate = rate if fx_operation == 'multiply' else Decimal(1) / rate
        value=p*currency_factor*applied_rate*target_kg/source_kg
        return {'date':date,'value':float(value),'value_decimal':str(value),
                'rounded_value':str(value.quantize(Decimal('0.01'),rounding=ROUND_HALF_UP)),
                'unit':target_unit,'fx_operation':fx_operation,'exchange_rate_direction':direction,
                'formula':f'{p} × {currency_factor} '+('×' if fx_operation == 'multiply' else '÷')+f' {rate} × {target_kg} ÷ {source_kg}',
                'price':price,'exchange_rate':fx,
                'notice':'纯币种及重量单位换算，不含关税、增值税、运费及其他通关成本。汇率方向来自已核对的来源元数据；公式源数据使用Excel保存结果，未重算。'}

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

    @contextmanager
    def _connect(self, dataset_id):
        path = self._path(dataset_id) / "data.duckdb"
        wal = Path(str(path) + ".wal")
        config = {"enable_external_access": "false", "threads": "2", "memory_limit": "256MB"}
        while True:
            with database_lock(path):
                if not wal.exists():
                    with closing(duckdb.connect(str(path), read_only=True, config=config)) as con:
                        yield con
                    return
            # Indexed WAL replay can need to write ART blocks to the main file.
            # Recover once with all readers excluded, then reopen strictly
            # read-only; propagate recovery errors without discarding the WAL.
            # A competing recovery may already have completed. Do not queue
            # behind readers of its clean snapshot; recheck under a shared lock.
            with database_lock(path, exclusive=True, blocking=False) as acquired:
                if acquired and wal.exists():
                    with closing(duckdb.connect(str(path), config=config)) as recovery:
                        recovery.execute("CHECKPOINT")
            if not acquired:
                time.sleep(0.01)

    def monthly_coverage(self, dataset_id, series_id, start_date, end_date):
        """Check full calendar months only for actual one-record-per-month series.

        Missing rows outside the file's date span are not counted by SQL NULL
        counts. Report them separately; never manufacture observations or zeros.
        """
        series=self._series(dataset_id,series_id)
        if series.get('chart_id') or (series.get('date_mode') or 'date')!='date':return None
        start=dt.date.fromisoformat(start_date);end=dt.date.fromisoformat(end_date)
        import calendar
        if start.day!=1 or end.day!=calendar.monthrange(end.year,end.month)[1]:return None
        with self._connect(dataset_id) as con:
            months=con.execute("SELECT strftime(date,'%Y-%m'), count(*), count(value), min(date) FROM observations WHERE series_id=? AND date IS NOT NULL GROUP BY 1 ORDER BY 1",[series_id]).fetchall()
        if len(months)<3 or any(r[1]!=1 for r in months):return None
        # Monthly sources may mix first-day and month-end date conventions.
        days={r[3].day for r in months}
        if len(days)>1 and not all(r[3].day in (1,calendar.monthrange(r[3].year,r[3].month)[1]) for r in months):return None
        expected=[];y,m=start.year,start.month
        while (y,m)<=(end.year,end.month):
            expected.append(f'{y:04d}-{m:02d}');y,m=(y+1,1) if m==12 else (y,m+1)
        present={r[0]:r for r in months if start<=r[3]<=end}
        valid=[m for m in expected if m in present and present[m][2]]
        missing=[m for m in expected if m not in valid]
        notice=f'请求 {len(expected)} 个月，原表有 {len(valid)} 个月有效记录'
        if valid:notice+='（'+valid[0]+' 至 '+valid[-1]+'）'
        if missing:notice+='；缺少有效数值的月份：'+'、'.join(missing[:24])+('等' if len(missing)>24 else '')+'，未补零'
        return {'frequency':'monthly','basis':'原表日期每月一条记录','expected_month_count':len(expected),'valid_month_count':len(valid),
                'missing_months':missing,'first_valid_month':valid[0] if valid else None,'last_valid_month':valid[-1] if valid else None,'complete':not missing,'notice':notice+'。'}

    def formula_examples(self, dataset_id, series_ids):
        """Expose bounded source expressions as metadata, never evaluate them."""
        ids=list(dict.fromkeys(series_ids))
        if not ids:return {}
        with self._connect(dataset_id) as con:
            rows=con.execute('SELECT series_id,min(formula) FROM observations WHERE series_id IN ('+','.join('?' for _ in ids)+') AND formula IS NOT NULL AND length(formula) BETWEEN 1 AND 400 GROUP BY series_id',ids).fetchall()
        return dict(rows)

    def query_series(self, dataset_id: str, series_id: str, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None, *, offset=0, value_filters=None, last_n=None, top_n=None, order='desc', _complete=False) -> dict:
        if type(offset) is not int or offset < 0 or offset > 10000000:
            raise ValueError('offset 必须为 0 至 10000000 的整数')
        if offset and (operation != 'rows' or _complete):
            raise ValueError('只有逐条查看记录支持分页')
        if _complete:
            if type(limit)!=int or not 1<=limit<=20000:raise ValueError('分析最多读取20000条原表记录')
        else:self._limit(limit)
        if operation not in {"rows", "latest", "mean", "sum", "min", "max", "count", "top"}:
            raise ValueError("query_series 的 arguments.operation 是统计方式，只支持 rows/latest/mean/sum/min/max/count/top；收到 "
                             + json.dumps(str(operation)[:40], ensure_ascii=False)
                             + "。工具名由外层 operation 指定，不放入 arguments；本次未查询数值。")
        if order not in ('asc','desc'):raise ValueError('排序方向无效')
        for n in (last_n,top_n):
            if n is not None and (type(n)!=int or not 1<=n<=200):raise ValueError('窗口或排序条数必须为1至200')
        filters=value_filters or []
        if not isinstance(filters,list) or len(filters)>8:raise ValueError('数值筛选条件无效')
        operators={'gt':'>','gte':'>=','lt':'<','lte':'<=','eq':'=','ne':'!='}
        import math
        for f in filters:
            if not isinstance(f,dict) or set(f)!={'op','value'} or f['op'] not in operators or type(f['value']) not in (int,float) or not math.isfinite(f['value']):raise ValueError('数值筛选条件无效')
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
            if last_n:
                if mode!='date':raise ValueError('日期窗口需要原表日期字段')
                window_where,window_params=where,list(params)
                where+=' AND date IN (SELECT DISTINCT date FROM observations WHERE '+window_where+' AND date IS NOT NULL ORDER BY date DESC LIMIT ?)'
                params.extend(window_params+[last_n])
            scope_count=con.execute(f'SELECT count(*) FROM observations WHERE {where}',params).fetchone()[0]
            for f in filters:
                where+=' AND value '+operators[f['op']]+' ?';params.append(f['value'])
            formula_count,valid_formula_count=con.execute(f"SELECT count(*),count(value) FROM observations WHERE {where} AND formula IS NOT NULL AND trim(formula) <> ''",params).fetchone()
            columns={r[1] for r in con.execute("PRAGMA table_info('observations')").fetchall()}
            extra=",period,period_cell,metric_cell" if 'period' in columns else ",NULL,NULL,NULL"
            count, valid, first, last, valid_first, valid_last = con.execute(f"SELECT count(*),count(value),min(date),max(date),min(date) FILTER(WHERE value IS NOT NULL),max(date) FILTER(WHERE value IS NOT NULL) FROM observations WHERE {where}", params).fetchone()
            observation_coverage = None
            if operation == 'rows':
                from .observation_coverage import summarize_observation_coverage
                observation_coverage = summarize_observation_coverage(
                    con, table='observations', where=where, params=params)
            aggregate = None
            extremum_count = None
            if operation not in {"rows", "latest", "top"}:
                function = {"mean": "avg", "sum": "sum", "min": "min", "max": "max", "count": "count"}[operation]
                aggregate = con.execute(f"SELECT {function}(value) FROM observations WHERE {where}", params).fetchone()[0]
            if operation == "latest":
                rows = con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} AND value IS NOT NULL ORDER BY date DESC LIMIT 1", params).fetchall()
            elif operation in {"min", "max"}:
                # Return the actual extremum rows with their dates, never early
                # source examples that a caller could pair with the aggregate.
                extremum_count=con.execute(f"SELECT count(*) FROM observations WHERE {where} AND value=?",params+[aggregate]).fetchone()[0]
                rows=con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} AND value=? ORDER BY date NULLS LAST,source_cell LIMIT ?",params+[aggregate,limit]).fetchall()
            elif operation=='top':
                rows=con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} AND value IS NOT NULL ORDER BY value {order.upper()},date,source_cell LIMIT ?",params+[top_n or limit]).fetchall()
            else:
                rows = con.execute(f"SELECT date,value,source_cell,value_status,formula,date_cell,raw_value{extra} FROM observations WHERE {where} ORDER BY date NULLS LAST,source_cell LIMIT ? OFFSET ?", params + [limit if operation == "rows" else 10, offset]).fetchall()
        if _complete and operation=='rows' and count>len(rows):raise ValueError('分析范围超过20000条，未使用截断样本替代完整计算；请缩小范围')
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
            warnings.append("当前字段目录尚未确认单位，不表示原件其他位置没有单位证据；可检查实际表头、脚注或与该字段范围对应的图表，并判断适用范围。没有实际依据前不补充或换算单位。")
        if count > valid:
            warnings.append(f"范围内 {count-valid} 条记录没有有效数值；统计排除这些记录。")
        if not valid:
            warnings.append("查询范围没有有效数据，不能用其他日期或模型记忆补齐。")
        if operation == "sum":
            warnings.append("结果是观测值的算术加总；库存、价格等指标是否适合加总需结合业务含义。")
        if operation in {'min','max'}:
            warnings.append('极值日期只能使用extremum_rows中的date；actual_first_date/actual_last_date是查询范围边界，不是极值日期。并列极值均保留，超出limit时标记截断。')
        if formula_count:
            warnings.append(f"查询范围内有 {formula_count} 条公式记录，其中 {valid_formula_count} 条有有效缓存数值；有效数值中另有 {valid-valid_formula_count} 条是非公式记录。仅公式结果来自原文件保存的缓存，本服务未重新计算 Excel 公式，不把普通数值称为公式缓存。")
        from .streaming import read_json_array
        for issue in read_json_array(self._path(dataset_id) / 'issues.json'):
            if issue.get('sheet') == series['sheet'] and issue.get('code') == 'source_zero_fill':
                warnings.append(issue['message'])
                break
        warnings.append("missing_count统计已导入记录中的空白、错误或无缓存值；不补建原表没有的记录。")
        filters = {"series_id": series_id, "start_date": start_date, "end_date": end_date, "operation": operation, "period":period, 'value_filters':value_filters or [], 'last_n':last_n,'top_n':top_n,'order':order}
        result = {"dataset_id": dataset_id, "status": "ok" if valid or (operation=='count' and scope_count) else "no_data", "series": series, "filters": filters, "scope_record_count":scope_count,"record_count": count, "valid_count": valid, "formula_count":formula_count, "valid_formula_count":valid_formula_count, "valid_non_formula_count":valid-valid_formula_count, "missing_count": count-valid, "actual_first_date": str(first) if first else None, "actual_last_date": str(last) if last else None, "valid_first_date":str(valid_first) if valid_first else None,"valid_last_date":str(valid_last) if valid_last else None, "aggregate": aggregate, "rows": records, "extremum_rows":records if operation in {'min','max'} else None, "extremum_count":extremum_count, "rows_are": "extremum_observations" if operation in {'min','max'} else "latest_valid_observation" if operation == "latest" else "observations" if operation in {'rows','top'} else "source_examples_not_all_inputs", "truncated": (extremum_count>len(records)) if extremum_count is not None else operation not in ('latest','top') and count > len(records), "source_semantics":{"date_representation":series.get('date_mode','date'),"statistical_calendar":series.get('statistical_calendar'),"aggregation_rule":series.get('aggregation_rule'),"notice":"未在源字段声明的业务年度、可加性与单位不能从名称推定为原表规则；采用背景常识时必须标为假设，并说明对范围/答案的影响。"}, "source": {"file": manifest["source_file"], "sha256": manifest["sha256"], "sheet": series["sheet"], "header_cells": series.get("header_cells", [])}, "warnings": warnings}
        if observation_coverage is not None:
            result['observation_coverage'] = observation_coverage
        if valid and records and not series.get('unit'):
            from .source_inspection import inspect_query_source_context
            result['source_evidence'] = inspect_query_source_context(
                self, dataset_id, series['sheet'], records, max_cells=8,
                max_chart_references=8,
                query_scope={'rows_are': result['rows_are'],
                             'record_count': count,
                             'truncated': result['truncated'],
                             'filters': filters})
        return result

    def compare_series(self, dataset_id: str, left_series_id: str, right_series_id: str, date: str, comparison_basis: str = 'same_explicit_unit') -> dict:
        if not self._date(date):
            raise ValueError("必须提供比较日期。")
        left = self.query_series(dataset_id, left_series_id, date, date)
        right = self.query_series(dataset_id, right_series_id, date, date)
        if left["status"] != "ok" or right["status"] != "ok":
            return {"status": "no_data", "difference": None, "left": left, "right": right}
        lu, ru = left["series"].get("unit"), right["series"].get("unit")
        if comparison_basis not in ('same_explicit_unit', 'raw_values'):
            raise ValueError('比较计划必须声明 same_explicit_unit 或 raw_values')
        raw_values = comparison_basis == 'raw_values'
        if (lu and ru and lu != ru) or (not raw_values and (not lu or not ru)):
            raise ValueError("两个指标单位不明确或不一致，不能自动计算差值；未确认单位须由计划明确声明原始数值比较。")
        return {"status": "ok", "definition": "left_minus_right", "difference": left["rows"][0]["value"]-right["rows"][0]["value"], "unit": lu if lu == ru else None,
                "comparison_basis": comparison_basis,
                "warnings": ["按计划仅比较原表保存的数值；未确认单位不补造、不换算，结果不构成已核验的业务口径。"] if raw_values else [],
                "left": left, "right": right}
