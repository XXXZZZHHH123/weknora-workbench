"""Full frozen structured exports, explicitly confirmed research mappings."""
import base64
import hashlib
import hmac
import json
import math
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import quote, urlsplit

import httpx
from sqlalchemy import select

from tin.config import settings, SHANGHAI
from tin.integration.identity import current_identity
from tin.models import Indicator, Observation, ObservationSource, SourceArtifact, SourceBatch, SourceMapping
from tin.ownership import verified_actor
from tin.provenance.service import artifact_path, preserve_blob, record_with_evidence, snapshot_workbook, stable_hash, start_batch, source_family, verified_artifact_bytes
from tin.schemas.caliber import Caliber
from tin.schemas.observation import ObservationIn


def structured_request(method, path, body=None):
    identity = current_identity.get()
    if identity is None:
        raise PermissionError("结构化桥接需要已核验账号和空间")
    base = settings.structured_data_url.rstrip("/")
    parsed = urlsplit(base)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.path or parsed.query or parsed.fragment or parsed.username:
        raise ValueError("结构化服务地址配置无效")
    content = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode() if body is not None else b""
    key = Path(settings.service_token_file).read_bytes().strip()
    if len(key) < 32:
        raise ValueError("结构化桥接服务密钥未配置")
    encoded = base64.urlsafe_b64encode(json.dumps({"user_id": identity.user_id, "email": identity.email,
        "display_name": identity.display_name, "tenant_id": identity.tenant_id, "role": identity.role},
        separators=(",", ":")).encode()).decode().rstrip("=")
    stamp = str(int(time.time()))
    message = "\n".join((stamp, method, path, encoded, hashlib.sha256(content).hexdigest()))
    signature = hmac.new(key, message.encode(), hashlib.sha256).hexdigest()
    response = httpx.request(method, base+path, content=content,
        headers={"Content-Type": "application/json", "X-Research-Timestamp": stamp,
                 "X-Research-Identity": encoded, "X-Research-Signature": signature},
        follow_redirects=False, trust_env=False, timeout=120)
    if response.status_code != 200:
        raise ValueError(f"结构化服务读取失败（HTTP {response.status_code}）")
    return response


def full_export(dataset_id, series_id):
    rows, offset, manifest, series, total = [], 0, None, None, None
    while True:
        page = structured_request("POST", "/api/research/export",
            {"dataset_id": dataset_id, "series_id": series_id, "offset": offset, "limit": 10000}).json()
        if type(page.get("total")) is not int or page["total"] < 0 or page["total"] > 2_000_000:
            raise ValueError("结构化导出规模不合法或超过 200 万行")
        if total is None:
            manifest, series, total = page["manifest"], page["series"], page["total"]
        if (page["manifest"] != manifest or page["series"] != series or page["total"] != total
                or page.get("offset") != offset or not isinstance(page.get("rows"), list)):
            raise ValueError("分页导出版本或记录数量变化，拒绝混合快照")
        rows.extend(page["rows"])
        if len(rows) > total:
            raise ValueError("结构化分页重复或超出总量")
        next_offset = page.get("next_offset")
        if next_offset is None:
            if page.get("truncated") or len(rows) != total:
                raise ValueError("结构化快照未完整导出，拒绝使用截断数据")
            break
        if not page["rows"] or next_offset != offset+len(page["rows"]):
            raise ValueError("结构化分页进度异常")
        offset = next_offset
    return {"manifest": manifest, "series": series, "rows": rows, "total": total}


def preview_bridge(session, dataset_id, series_id):
    exported = full_export(dataset_id, series_id)
    manifest, spec = exported["manifest"], exported["series"]
    original = structured_request("GET", "/api/research/source/"+quote(dataset_id, safe="")).content
    if hashlib.sha256(original).hexdigest() != manifest.get("sha256"):
        raise ValueError("结构化快照原件哈希不匹配，拒绝桥接")
    artifact = preserve_blob(session, original, manifest["source_file"], kind="structured",
                             metadata={"dataset_id": dataset_id, "manifest": manifest})
    snapshot_workbook(session, artifact)
    snapshot = preserve_blob(session, json.dumps(exported, ensure_ascii=False, default=str).encode(),
        f"{dataset_id}-{series_id}.json", kind="structured_snapshot", metadata={"original_artifact_id": artifact.id})
    # Coverage changes when rows grow; semantic changes require a fresh review.
    variable_fields = {"record_count", "valid_count", "first_date", "last_date", "first_valid_date",
                       "last_valid_date", "periods", "category_provenance"}
    semantic_spec = {key: value for key, value in spec.items() if key not in variable_fields}
    fingerprint = stable_hash({"bridge": "v1", "source_family": source_family(manifest["source_file"]),
                               "semantic_spec": semantic_spec})
    profile = session.get(SourceMapping, fingerprint)
    batch = start_batch(session, artifact, kind="bridge", key={"artifact": artifact.id, "series": series_id,
        "dataset_id": dataset_id, "snapshot": snapshot.id},
        mapping={"external_snapshot_id": snapshot.id, "external_series_id": series_id, "dataset_id": dataset_id,
                 "fingerprint": fingerprint})
    session.commit()
    return {"batch_id": batch.id, "artifact_id": artifact.id, "sha256": artifact.id,
            "manifest": manifest, "series": spec, "total": exported["total"],
            "known_mapping": profile.config if profile else None, "status": batch.status,
            "sample_rows": exported["rows"][:10], "sample_is_full_export": False}


def apply_bridge(session, batch_id, mapping=None, *, allow_conflicts=False):
    batch = session.get(SourceBatch, batch_id)
    if batch is None or batch.kind != "bridge":
        raise ValueError("桥接批次不存在")
    verified_artifact_bytes(session, session.get(SourceArtifact, batch.artifact_id))
    snapshot_bytes = verified_artifact_bytes(session, session.get(SourceArtifact, batch.mapping["external_snapshot_id"]))
    if batch.status == "committed":
        return {**batch.result, "written": 0, "evidence_rows": 0, "already_imported": True, "previous_result": batch.result}
    profile = session.get(SourceMapping, batch.mapping["fingerprint"])
    config = mapping or (profile.config if profile else None)
    if not config:
        raise ValueError("该格式尚未确认研究指标与单位映射")
    ind = session.get(Indicator, config.get("research_series_id"))
    if ind is None or ind.status != "可用":
        raise ValueError("目标研究指标未登记或已停用")
    factor = config.get("factor", 1)
    if isinstance(factor, bool) or not isinstance(factor, (float, int)) or not math.isfinite(factor) or factor <= 0:
        raise ValueError("单位换算系数必须为明确正有限数")
    if not config.get("source_unit") or config.get("target_unit") != ind.unit or not config.get("note"):
        raise ValueError("请明确原单位、登记单位和映射说明")
    if not mapping and config.get("caliber") != ind.caliber:
        raise ValueError("指标口径已改变，需要重新确认映射")
    config = {**config, "caliber": ind.caliber, "target_unit": ind.unit, "factor": factor}
    data = json.loads(snapshot_bytes)
    if data["series"].get("date_mode") in {"none", "period"}:
        raise ValueError("源字段没有明确日期，不能推断研究观测时点；请先确认日期或统计期映射")
    if data["series"].get("unit") and config["source_unit"] != data["series"]["unit"]:
        raise ValueError("声明的原单位与冻结字段元信息不符")
    artifact = session.get(SourceArtifact, batch.artifact_id)
    verified_artifact_bytes(session, artifact)
    pending, conflicts, skipped = [], [], []
    from tin.ingest.excel_importer import parse_as_of
    for raw in data["rows"]:
        if raw.get("value_status") not in {"valid", "ok", "正常"} or raw.get("value") is None:
            skipped.append({"source_cell": raw.get("source_cell"), "status": raw.get("value_status"), "reason": "无有效数值；未补零"})
            continue
        date_value = raw.get("date")
        if not date_value:
            skipped.append({"source_cell": raw.get("source_cell"), "reason": "日期/统计期未明确映射，未推断时点"})
            continue
        try:
            when, filled = parse_as_of(datetime.fromisoformat(date_value) if "T" in str(date_value) else date_value,
                                      ind.default_entry_time)
            value = float(raw["value"]) * factor
            if not math.isfinite(value):
                raise ValueError("非有限数值")
        except (ValueError, TypeError):
            skipped.append({"source_cell": raw.get("source_cell"), "reason": "原值/日期无法转换"})
            continue
        prior = session.scalar(select(Observation).where(Observation.series_id == ind.series_id,
            Observation.as_of == when).order_by(Observation.revision.desc()).limit(1))
        if prior is not None and prior.value != value:
            same_lineage = profile is not None and profile.config == config and any(
                e.transformation.get("format_fingerprint") == batch.mapping["fingerprint"]
                for e in session.scalars(select(ObservationSource).where(ObservationSource.observation_id==prior.id)))
            if not same_lineage:
                conflicts.append({"source_cell": raw.get("source_cell"), "as_of": when.isoformat(),
                                  "old_value": prior.value, "new_value": value})
        pending.append((raw, when, value, filled))
    # Duplicates within a snapshot are also conflicts; never use row order to decide truth.
    unique = {}
    for raw, when, value, _ in pending:
        if when in unique and unique[when] != value:
            raise ValueError(f"快照在 {when.isoformat()} 包含互相冲突的值；请先修正或分别映射")
        unique[when] = value
    if conflicts and not allow_conflicts:
        return {"status": "needs_confirmation", "conflicts": conflicts, "written": 0, "skipped": skipped}
    if profile is None:
        profile = SourceMapping(id=batch.mapping["fingerprint"], config=config, version=1,
            confirmed_by=verified_actor(session, "研究员"), confirmed_at=datetime.now(timezone.utc))
        session.add(profile)
    elif profile.config != config:
        profile.config, profile.version = config, profile.version+1
        profile.confirmed_by, profile.confirmed_at = verified_actor(session, "研究员"), datetime.now(timezone.utc)
    written, evidence = 0, 0
    for raw, when, value, filled in pending:
        obs = ObservationIn(series_id=ind.series_id, value=value, as_of=when,
            caliber=Caliber.model_validate(ind.caliber), source="结构化文件", source_url=None,
            entered_by=verified_actor(session, "研究员"), note=config["note"])
        _, added = record_with_evidence(session, obs, artifact=artifact, batch_id=batch.id,
            locator={"type": "structured_cell", "dataset_id": batch.mapping["dataset_id"],
                     "filename": data["manifest"]["source_file"],
                     "series_id": batch.mapping["external_series_id"], "sheet": data["series"].get("sheet"),
                     "value_cell": raw.get("source_cell"), "date_cell": raw.get("date_cell"),
                     "header_cells": data["series"].get("header_cells"), "raw_value": raw.get("raw_value"),
                     "formula": raw.get("formula"), "value_status": raw.get("value_status"),
                     "date": raw.get("date"), "period": raw.get("period")},
            mapping_version=f"bridge-v1:{profile.version}:"+stable_hash({"dataset": batch.mapping["dataset_id"],
                "snapshot": batch.mapping["external_snapshot_id"], "caliber": config["caliber"]})[:32], transformation={"mapping": config,
                "time_filled": filled, "format_fingerprint": batch.mapping["fingerprint"],
                "snapshot_sha256": batch.mapping["external_snapshot_id"]})
        written += added; evidence += 1
    batch.result = {"status": "committed", "written": written, "evidence_rows": evidence,
                    "skipped": skipped, "conflicts": conflicts, "mapping_version": profile.version}
    batch.status, batch.committed_at = "committed", datetime.now(timezone.utc)
    session.commit()
    return batch.result
