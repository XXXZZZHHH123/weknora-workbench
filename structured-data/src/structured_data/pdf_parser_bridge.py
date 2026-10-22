"""Bounded bridge to a server-configured, self-hosted PDF layout parser.

The bridge deliberately consumes structured layout JSON (or HTML table cells)
and never infers page/cell locations from Markdown or from a parser's merged
cross-page rendering. Endpoints and credentials come from server environment
configuration; request data cannot select a backend or destination.
"""
from __future__ import annotations

import asyncio
import base64
import hashlib
import html
from html.parser import HTMLParser
import io
import json
import os
from pathlib import Path
import re
import time
from typing import Any, Mapping
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urljoin, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
import zipfile


SCHEMA_VERSION = "pdf_tables_v1"
DEFAULT_MAX_FILE_BYTES = 50 * 1024 * 1024
DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024
DEFAULT_MAX_ARCHIVE_BYTES = 64 * 1024 * 1024
DEFAULT_TIMEOUT_SECONDS = 600
DEFAULT_MAX_POLLS = 40
DEFAULT_POLL_INTERVAL_SECONDS = 2
_SAFE_ID = re.compile(r"[^A-Za-z0-9._-]+")


class PDFParserError(RuntimeError):
    """Raised when a configured parser cannot provide trustworthy layout data."""


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req: Request, fp: Any, code: int, msg: str,
                         headers: Any, newurl: str) -> None:
        return None


class ParserConfig:
    """Trusted, server-owned backend configuration.

    ``from_env`` is the production constructor. Passing a config explicitly is
    intended for a server integration or isolated tests, never request fields.
    """

    def __init__(self, engine: str, url: str, *, api_key: str = "", tier: str = "",
                 ocr_mode: str = "auto", timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS,
                 max_file_bytes: int = DEFAULT_MAX_FILE_BYTES,
                 max_response_bytes: int = DEFAULT_MAX_RESPONSE_BYTES,
                 max_archive_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES,
                 max_polls: int = DEFAULT_MAX_POLLS,
                 poll_interval_seconds: float = DEFAULT_POLL_INTERVAL_SECONDS,
                 paddle_params: Mapping[str, Any] | None = None):
        self.engine = str(engine).strip().lower()
        if self.engine not in {"mineru", "mineru_v1", "mineru_legacy", "paddleocr_vl"}:
            raise ValueError("PDF_TABLE_PARSER_ENGINE must be mineru_v1, mineru_legacy, or paddleocr_vl")
        self.url = _validate_base_url(url)
        self.api_key = str(api_key or "").strip()
        self.tier = tier if tier in {"", "flash", "basic", "standard", "advanced"} else ""
        self.ocr_mode = ocr_mode if ocr_mode in {"auto", "txt", "ocr"} else "auto"
        self.timeout_seconds = _bounded_int(timeout_seconds, 1, 1800, "timeout_seconds")
        self.max_file_bytes = _bounded_int(max_file_bytes, 1, 100 * 1024 * 1024, "max_file_bytes")
        self.max_response_bytes = _bounded_int(max_response_bytes, 1024, 64 * 1024 * 1024, "max_response_bytes")
        self.max_archive_bytes = _bounded_int(max_archive_bytes, 1024, 128 * 1024 * 1024, "max_archive_bytes")
        self.max_polls = _bounded_int(max_polls, 1, 100, "max_polls")
        self.poll_interval_seconds = min(max(float(poll_interval_seconds), 0.05), 30.0)
        self.paddle_params = dict(paddle_params or {})

    @classmethod
    def from_env(cls) -> "ParserConfig":
        engine = os.getenv("PDF_TABLE_PARSER_ENGINE", "").strip()
        endpoint = os.getenv("PDF_TABLE_PARSER_URL", "").strip()
        if not engine or not endpoint:
            raise PDFParserError("PDF table parser is not configured (PDF_TABLE_PARSER_ENGINE and PDF_TABLE_PARSER_URL required)")
        return cls(
            engine, endpoint,
            api_key=os.getenv("PDF_TABLE_PARSER_API_KEY", ""),
            tier=os.getenv("PDF_TABLE_PARSER_TIER", ""),
            ocr_mode=os.getenv("PDF_TABLE_PARSER_OCR_MODE", "auto"),
            timeout_seconds=_env_int("PDF_TABLE_PARSER_TIMEOUT_SECONDS", DEFAULT_TIMEOUT_SECONDS),
            max_file_bytes=_env_int("PDF_TABLE_PARSER_MAX_FILE_BYTES", DEFAULT_MAX_FILE_BYTES),
            max_response_bytes=_env_int("PDF_TABLE_PARSER_MAX_RESPONSE_BYTES", DEFAULT_MAX_RESPONSE_BYTES),
            max_archive_bytes=_env_int("PDF_TABLE_PARSER_MAX_ARCHIVE_BYTES", DEFAULT_MAX_ARCHIVE_BYTES),
            max_polls=_env_int("PDF_TABLE_PARSER_MAX_POLLS", DEFAULT_MAX_POLLS),
            poll_interval_seconds=_env_float("PDF_TABLE_PARSER_POLL_INTERVAL_SECONDS", DEFAULT_POLL_INTERVAL_SECONDS),
        )

    def availability(self) -> dict[str, Any]:
        """Safe diagnostics; never expose URL, API key, or other endpoint details."""
        return {"configured": True, "engine": _engine_name(self.engine)}


def parser_availability() -> dict[str, Any]:
    try:
        return ParserConfig.from_env().availability()
    except (ValueError, PDFParserError):
        return {"configured": False, "engine": None}


async def parse_pdf(path: str | os.PathLike[str], source: Mapping[str, Any] | None = None,
                    config: ParserConfig | None = None) -> dict[str, Any]:
    """Parse one local PDF and return a normalized ``pdf_tables_v1`` bundle.

    ``source`` may contain ``file_name`` and ``knowledge_id``. Its optional
    ``sha256`` must match the actual bytes. Request-provided URLs/credentials
    are ignored because no backend settings are read from source metadata.
    """
    cfg = config or ParserConfig.from_env()
    started = time.monotonic()
    pdf_path = Path(path)
    try:
        stat = pdf_path.stat()
    except OSError as exc:
        raise PDFParserError(f"cannot read PDF input: {exc}") from exc
    if not pdf_path.is_file() or stat.st_size <= 0:
        raise PDFParserError("PDF input is empty or not a regular file")
    if stat.st_size > cfg.max_file_bytes:
        raise PDFParserError(f"PDF input exceeds configured limit ({cfg.max_file_bytes} bytes)")
    raw_pdf = await asyncio.to_thread(pdf_path.read_bytes)
    if len(raw_pdf) != stat.st_size or len(raw_pdf) > cfg.max_file_bytes:
        raise PDFParserError("PDF input changed or exceeded its size limit while being read")
    if not raw_pdf.startswith(b"%PDF-"):
        raise PDFParserError("input is not a PDF file")
    actual_sha = hashlib.sha256(raw_pdf).hexdigest()
    source = dict(source or {})
    given_sha = source.get("sha256")
    if given_sha is not None and str(given_sha).lower() != actual_sha:
        raise PDFParserError("source sha256 does not match PDF bytes")
    file_name = _safe_filename(source.get("file_name") or pdf_path.name)
    if not file_name.lower().endswith(".pdf"):
        file_name += ".pdf"

    if cfg.engine in {"mineru", "mineru_v1"}:
        response_meta, parsed = await _mineru_v1(raw_pdf, file_name, cfg, started)
        response = {**response_meta, "structured": parsed.get("layout"),
                    "markdown": parsed.get("markdown", "")}
    elif cfg.engine == "mineru_legacy":
        response = await asyncio.to_thread(_mineru_legacy, raw_pdf, file_name, cfg)
    else:
        response = await asyncio.to_thread(_paddle_layout, raw_pdf, cfg)
    bundle = normalize_parser_response(cfg.engine, response, {
        "sha256": actual_sha, "file_name": file_name,
        "knowledge_id": source.get("knowledge_id"),
        "knowledge_base_id": source.get("knowledge_base_id"),
    }, max_artifact_bytes=cfg.max_response_bytes)
    bundle["parse_duration_ms"] = int((time.monotonic() - started) * 1000)
    return bundle


def normalize_parser_response(engine: str, response: Mapping[str, Any],
                              source: Mapping[str, Any], *,
                              max_artifact_bytes: int = DEFAULT_MAX_RESPONSE_BYTES) -> dict[str, Any]:
    """Normalize a captured backend response; useful for store integration tests.

    ``mineru_v1`` input is the bridge envelope with ``structured`` and
    optional ``markdown``. ``mineru_legacy`` is a single MinerU result entry.
    ``paddleocr_vl`` is the /layout-parsing response object.
    """
    canonical = str(engine).strip().lower()
    if canonical in {"mineru", "mineru_v1"}:
        layout = response.get("structured")
        if layout is None:
            raise PDFParserError("MinerU V1 response is missing structured layout JSON")
        fragments, formulas = _mineru_tables(layout)
        raw_artifact = {"structured": layout, "markdown": response.get("markdown", "")}
    elif canonical == "mineru_legacy":
        layout = response.get("content_list")
        if layout is None:
            raise PDFParserError("MinerU legacy response is missing structured content_list")
        fragments, formulas = _mineru_tables(layout)
        raw_artifact = {"structured": layout, "markdown": response.get("md_content", ""),
                        "response_json": response}
    elif canonical == "paddleocr_vl":
        result = response.get("result")
        pages = result.get("layoutParsingResults") if isinstance(result, Mapping) else None
        if not isinstance(pages, list) or not pages:
            raise PDFParserError("PaddleOCR-VL response is missing result.layoutParsingResults")
        fragments, formulas = _paddle_tables(pages)
        raw_artifact = {"response_json": response}
    else:
        raise PDFParserError("unsupported parser engine")
    if not fragments:
        raise PDFParserError("parser returned no structured table cells; Markdown-only tables are not accepted")
    fragments, ambiguities = _mark_ambiguous_cross_page(fragments)
    source_bundle = {
        "sha256": str(source.get("sha256", "")).lower(),
        "file_name": _safe_filename(source.get("file_name") or "document.pdf"),
        "knowledge_id": _source_id(source.get("knowledge_id")),
        "knowledge_base_id": _source_id(source.get("knowledge_base_id")),
    }
    if not re.fullmatch(r"[0-9a-f]{64}", source_bundle["sha256"]):
        raise PDFParserError("source sha256 must be a 64-character lowercase hex digest")
    if ambiguities and not fragments:
        raise PDFParserError("structured tables lack parser-supplied page provenance; review is required")
    bundle = {
        "schema_version": SCHEMA_VERSION,
        "source": source_bundle,
        "parser": {"engine": _engine_name(canonical), "version": _parser_version(response)},
        "fragments": fragments,
        "formulas": formulas,
        "raw": _json_safe_artifact(raw_artifact, max_artifact_bytes),
    }
    if ambiguities:
        bundle["ambiguities"] = ambiguities
        bundle["completeness"] = "unknown"
        bundle["requires_review"] = True
    return bundle


def _engine_name(engine: str) -> str:
    return "mineru" if engine in {"mineru", "mineru_v1", "mineru_legacy"} else "paddleocr_vl"


def _parser_version(response: Mapping[str, Any]) -> str | None:
    for key in ("parser_version", "version", "model_version"):
        value = response.get(key)
        if isinstance(value, str) and value:
            return value[:128]
    structured = response.get("structured")
    if isinstance(structured, dict):
        producer = (structured.get("metadata") or {}).get("producer")
        if isinstance(producer, dict) and isinstance(producer.get("version"), str):
            return producer["version"][:128]
    return None


async def _mineru_v1(content: bytes, filename: str, cfg: ParserConfig,
                     started: float) -> tuple[dict[str, Any], Any]:
    deadline = started + cfg.timeout_seconds
    api_headers = {"Authorization": f"Bearer {cfg.api_key}"} if cfg.api_key else {}
    sha = hashlib.sha256(content).hexdigest()
    upload = await asyncio.to_thread(_json_request, cfg, "POST", "/v1/uploads", {
        "filename": filename, "bytes": len(content), "mime_type": "application/pdf",
        "purpose": "parse", "sha256sum": sha,
    }, api_headers, deadline)
    if upload.get("status") == "completed":
        file_obj = upload.get("file")
        file_id = file_obj.get("id") if isinstance(file_obj, dict) else None
    elif upload.get("status") == "pending":
        upload_id = upload.get("id")
        target = upload.get("upload_url")
        if not upload_id or not target:
            raise PDFParserError("MinerU pending upload omitted id or upload_url")
        target_url = _trusted_upload_url(cfg.url, str(target))
        method = str(upload.get("upload_method") or "PUT").upper()
        if method not in {"PUT", "POST"}:
            raise PDFParserError("MinerU upload method is not allowed")
        headers = upload.get("upload_headers") if isinstance(upload.get("upload_headers"), dict) else {}
        # The server may request content-type; it cannot override host or auth.
        safe_headers = {str(k): str(v) for k, v in headers.items()
                        if str(k).lower() not in {"authorization", "host", "cookie"}}
        await asyncio.to_thread(_request, cfg, method, target_url, content,
                                {"Content-Type": "application/pdf", **safe_headers}, deadline=deadline)
        completed = await asyncio.to_thread(_json_request, cfg, "POST",
                                             f"/v1/uploads/{quote(str(upload_id), safe='')}/complete",
                                             {"sha256sum": sha}, api_headers, deadline)
        f = completed.get("file")
        file_id = f.get("id") if isinstance(f, dict) else None
    else:
        raise PDFParserError("MinerU returned an unsupported upload status")
    if not file_id:
        raise PDFParserError("MinerU upload did not return a file id")
    body: dict[str, Any] = {"files": [{"source": {"type": "file_id", "file_id": str(file_id)}}],
                            "output_formats": ["zip"], "ocr_mode": cfg.ocr_mode}
    if cfg.tier:
        body["tier"] = cfg.tier
    job = await asyncio.to_thread(_json_request, cfg, "POST", "/v1/parse/jobs", body, api_headers, deadline)
    job_id = job.get("job_id")
    if not job_id:
        raise PDFParserError("MinerU parse job response omitted job_id")
    terminal = {"completed", "partial", "failed", "canceled"}
    for attempt in range(cfg.max_polls):
        if time.monotonic() >= deadline:
            raise PDFParserError("MinerU parse exceeded configured total timeout")
        job = await asyncio.to_thread(_json_request, cfg, "GET",
                                      f"/v1/parse/jobs/{quote(str(job_id), safe='')}", None, api_headers, deadline)
        status = str(job.get("status", "")).lower()
        if status in terminal:
            break
        await asyncio.sleep(min(cfg.poll_interval_seconds, max(0.0, deadline - time.monotonic())))
    else:
        raise PDFParserError("MinerU parse exceeded configured poll limit")
    files = job.get("files")
    file_result = files[0] if isinstance(files, list) and files else None
    if not isinstance(file_result, dict) or file_result.get("status") != "completed":
        detail = file_result.get("error") if isinstance(file_result, dict) else None
        raise PDFParserError(f"MinerU parse did not complete: {detail or job.get('status')}")
    output_files = file_result.get("output_files") or {}
    zip_ref = output_files.get("zip") if isinstance(output_files, dict) else None
    artifact_id = zip_ref.get("file_id") if isinstance(zip_ref, dict) else None
    if not artifact_id:
        raise PDFParserError("MinerU completed without a zip artifact")
    archive = await asyncio.to_thread(_request, cfg, "GET",
                                      _join(cfg.url, f"/v1/files/{quote(str(artifact_id), safe='')}/content"), None, api_headers,
                                      cfg.max_archive_bytes, deadline)
    layout = _read_mineru_zip(archive, cfg.max_archive_bytes)
    markdown = _zip_markdown(archive, cfg.max_archive_bytes)
    return {"parser_version": _parser_version(file_result.get("parse") or {})}, {"layout": layout, "markdown": markdown}


def _mineru_legacy(content: bytes, filename: str, cfg: ParserConfig) -> dict[str, Any]:
    boundary = "----pdfbridge" + hashlib.sha256(content[:128]).hexdigest()[:24]
    fields = {
        "return_md": "true", "return_images": "false", "table_enable": "true",
        "formula_enable": "true", "parse_method": cfg.ocr_mode,
        "response_format_zip": "false", "return_content_list": "true",
        "start_page_id": "0", "end_page_id": "99999", "backend": "pipeline",
    }
    parts: list[bytes] = []
    for name, value in fields.items():
        parts.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode())
    safe_filename = filename.replace('"', "_").replace("\r", "_").replace("\n", "_")
    parts.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"files\"; filename=\"{safe_filename}\"\r\nContent-Type: application/pdf\r\n\r\n".encode())
    parts.append(content)
    parts.append(f"\r\n--{boundary}--\r\n".encode())
    raw = _request(cfg, "POST", _join(cfg.url, "/file_parse"), b"".join(parts),
                   {"Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        envelope = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise PDFParserError("MinerU legacy response is not valid JSON") from exc
    results = envelope.get("results") if isinstance(envelope, dict) else None
    if not isinstance(results, dict) or not results:
        raise PDFParserError("MinerU legacy response is missing results")
    # A one-file request must have one result. Avoid filename-stem guessing.
    entry = next(iter(results.values()))
    if not isinstance(entry, dict):
        raise PDFParserError("MinerU legacy result entry is invalid")
    return entry


def _paddle_layout(content: bytes, cfg: ParserConfig) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "file": base64.b64encode(content).decode("ascii"), "fileType": 0, "visualize": False,
        "markdownIgnoreLabels": ["header", "header_image", "footer", "footer_image", "number", "aside_text"],
        "useDocOrientationClassify": False, "useDocUnwarping": False,
        "useLayoutDetection": True, "useChartRecognition": False,
        "useSealRecognition": True, "useOcrForImageBlock": False,
        "mergeTables": False, "relevelTitles": True, "restructurePages": True,
        "layoutShapeMode": "auto", "promptLabel": "ocr", "layoutNms": True,
        "repetitionPenalty": 1, "temperature": 0, "topP": 1,
        "minPixels": 147384, "maxPixels": 2822400,
    }
    # Only explicitly documented, server-side allowlisted recognizer parameters.
    for key in ("useChartRecognition", "useSealRecognition", "minPixels", "maxPixels"):
        if key in cfg.paddle_params:
            payload[key] = cfg.paddle_params[key]
    raw = _request(cfg, "POST", _join(cfg.url, "/layout-parsing"),
                   json.dumps(payload, separators=(",", ":")).encode(),
                   {"Content-Type": "application/json"})
    try:
        response = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise PDFParserError("PaddleOCR-VL response is not valid JSON") from exc
    if not isinstance(response, dict) or response.get("errorCode", 0) not in (0, None):
        raise PDFParserError(f"PaddleOCR-VL returned an error: {response.get('errorMsg') if isinstance(response, dict) else 'invalid response'}")
    return response


def _json_request(cfg: ParserConfig, method: str, path: str, payload: Any,
                  headers: Mapping[str, str], deadline: float | None = None) -> dict[str, Any]:
    data = json.dumps(payload, separators=(",", ":")).encode() if payload is not None else None
    req_headers = dict(headers)
    if data is not None:
        req_headers["Content-Type"] = "application/json"
    raw = _request(cfg, method, _join(cfg.url, path), data, req_headers, deadline=deadline)
    try:
        result = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise PDFParserError(f"MinerU {path} response is not valid JSON") from exc
    if not isinstance(result, dict):
        raise PDFParserError(f"MinerU {path} response must be an object")
    return result


def _request(cfg: ParserConfig, method: str, url: str, data: bytes | None,
             headers: Mapping[str, str] | None = None, max_bytes: int | None = None,
             deadline: float | None = None) -> bytes:
    limit = min(max_bytes or cfg.max_response_bytes, cfg.max_response_bytes if max_bytes is None else max_bytes)
    if data is not None and len(data) > cfg.max_file_bytes * 2 + 1024 * 1024:
        raise PDFParserError("parser request body exceeds configured limit")
    req = Request(url, data=data, headers=dict(headers or {}), method=method)
    opener = build_opener(_NoRedirect(), ProxyHandler({}))
    request_timeout = float(cfg.timeout_seconds)
    if deadline is not None:
        request_timeout = min(request_timeout, deadline - time.monotonic())
        if request_timeout <= 0:
            raise PDFParserError("parser exceeded configured total timeout")
    try:
        with opener.open(req, timeout=request_timeout) as resp:
            if not 200 <= resp.status < 300:
                raise PDFParserError(f"parser returned HTTP {resp.status}")
            length = resp.headers.get("Content-Length")
            if length and int(length) > limit:
                raise PDFParserError("parser response exceeds configured limit")
            body = resp.read(limit + 1)
            if len(body) > limit:
                raise PDFParserError("parser response exceeds configured limit")
            return body
    except HTTPError as exc:
        # Do not follow redirects, including signed URLs from parser responses.
        raise PDFParserError(f"parser returned HTTP {exc.code}; redirects are disabled") from exc
    except (URLError, TimeoutError, OSError) as exc:
        raise PDFParserError(f"parser request failed: {exc}") from exc


def _join(base: str, route: str) -> str:
    return base.rstrip("/") + "/" + route.lstrip("/")


def _validate_base_url(raw: str) -> str:
    parts = urlsplit(str(raw).strip())
    if parts.scheme not in {"http", "https"} or not parts.hostname:
        raise ValueError("parser URL must be an absolute HTTP(S) URL")
    if parts.username or parts.password or parts.query or parts.fragment:
        raise ValueError("parser URL must not contain credentials, query, or fragment")
    return str(raw).strip().rstrip("/")


def _trusted_upload_url(base: str, raw: str) -> str:
    resolved = urljoin(base.rstrip("/") + "/", raw)
    a, b = urlsplit(base), urlsplit(resolved)
    if b.scheme not in {"http", "https"} or b.username or b.password or b.hostname != a.hostname or b.port != a.port or b.scheme != a.scheme:
        raise PDFParserError("MinerU upload URL is outside the configured service origin")
    return resolved


def _mineru_tables(layout: Any) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    if isinstance(layout, dict) and "layout" in layout:
        layout = layout["layout"]
    if isinstance(layout, str):
        try:
            layout = json.loads(layout)
        except json.JSONDecodeError as exc:
            raise PDFParserError("MinerU content_list is not structured JSON") from exc
    blocks: list[tuple[int | None, list[float] | None, Any, str | None, list[str]]] = []
    formulas: list[dict[str, Any]] = []
    if isinstance(layout, list):
        for item in layout:
            if not isinstance(item, dict):
                continue
            page = item.get("page_idx")
            page = int(page) + 1 if isinstance(page, int) and page >= 0 else None
            if str(item.get("type", "")).lower() == "table" or item.get("table_body"):
                box = _box(item.get("bbox"), scale=1000.0)
                blocks.append((page, box,
                               item.get("table_body") or item.get("html") or item.get("rows") or item.get("cells"),
                               _raw_text(item.get("table_caption") or item.get("caption")),
                               _raw_text_list(item.get("table_footnote") or item.get("footnotes"))))
            formulas.extend(_formula_objects(item.get("formula"), item.get("page_idx"), item.get("bbox")))
            if str(item.get("type", "")).lower() in {"formula", "equation"}:
                formula = _formula(item, page, item.get("bbox"))
                if formula:
                    formulas.append(formula)
    elif isinstance(layout, dict) and layout.get("schema") == "docvortex.middle":
        for page_doc in layout.get("pages", []):
            if not isinstance(page_doc, dict):
                continue
            p = page_doc.get("page_idx")
            page = int(p) + 1 if isinstance(p, int) and p >= 0 else None
            for block in page_doc.get("blocks", []):
                if not isinstance(block, dict):
                    continue
                typ = str(block.get("type", "")).lower()
                content = block.get("content")
                if typ == "table":
                    blocks.append((page, _box(block.get("bbox")), _find_html_or_rows(content),
                                   _layout_annotation(content, {"table_caption", "caption"}),
                                   _layout_annotations(content, {"table_footnote", "footnote", "footnotes"})))
                if typ in {"formula", "equation"}:
                    formulas.extend(_formula_objects(content, page - 1 if page else None, block.get("bbox")))
    elif not isinstance(layout, list):
        raise PDFParserError("MinerU content_list has no supported structured layout")
    fragments = []
    for index, (page, bbox, table, caption, footnotes) in enumerate(blocks, 1):
        grid, source_html = _parse_table(table)
        if not grid:
            continue
        fragment = _fragment(page, bbox, grid, source_html, fragment_id=f"table-{index:04d}")
        if caption:
            fragment["caption"] = caption
        if footnotes:
            fragment["footnotes"] = footnotes
        fragments.append(fragment)
    return fragments, formulas


def _paddle_tables(pages: list[Any]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    fragments: list[dict[str, Any]] = []
    formulas: list[dict[str, Any]] = []
    for page_index, page in enumerate(pages, 1):
        if not isinstance(page, dict):
            continue
        raw_pruned = page.get("prunedResult")
        if isinstance(raw_pruned, str):
            try:
                raw_pruned = json.loads(raw_pruned)
            except json.JSONDecodeError:
                continue
        if not isinstance(raw_pruned, dict):
            continue
        width, height = raw_pruned.get("width"), raw_pruned.get("height")
        fragments_before_page = len(fragments)
        for idx, block in enumerate(raw_pruned.get("parsing_res_list") or []):
            if not isinstance(block, dict):
                continue
            label = str(block.get("block_label") or block.get("label") or "").lower()
            content = block.get("block_content") or block.get("html") or block.get("table")
            if label == "table" or _contains_table(content) or _has_rows(block):
                candidate = content if content is not None else (block.get("rows") or block.get("cells"))
                grid, source_html = _parse_table(
                    _find_html_or_rows(candidate) if content is not None else candidate,
                    bbox_scale=(width, height) if width and height else None,
                )
                if not grid:
                    continue
                coords = block.get("block_bbox") or block.get("bbox")
                bbox = _box(coords, scale=(width, height))
                fragments.append(_fragment(page_index, bbox, grid, source_html,
                                          fragment_id=block.get("id") or block.get("block_id") or f"paddle-{page_index}-{idx + 1}"))
            if label in {"formula", "equation"}:
                formula = _formula(block, page_index, block.get("block_bbox") or block.get("bbox"), width, height)
                if formula:
                    formulas.append(formula)
        # Some serving versions put table objects directly on each page.
        for idx, table in enumerate(page.get("tables") or []) if len(fragments) == fragments_before_page else []:
            if not isinstance(table, dict):
                continue
            grid, source_html = _parse_table(_find_html_or_rows(table),
                                             bbox_scale=(width, height) if width and height else None)
            if grid:
                bbox = _box(table.get("bbox"), scale=(width, height))
                fragments.append(_fragment(page_index, bbox, grid, source_html,
                                          fragment_id=table.get("id") or f"paddle-{page_index}-table-{idx + 1}"))
    return fragments, formulas


def _find_html_or_rows(value: Any) -> Any:
    if isinstance(value, dict):
        for key in ("html", "table_html", "table_body", "rows", "cells"):
            if key in value:
                return value[key]
        if "content" in value:
            return _find_html_or_rows(value["content"])
        # MiddleJson content can be an object containing child blocks.
        for child in value.values():
            found = _find_html_or_rows(child)
            if found is not None:
                return found
    if isinstance(value, list):
        # MinerU MiddleJson groups table_caption, table_body, and footnotes
        # under one table block. The first nested item is often a caption, so
        # generic first-child recursion mistakes caption text for cell data.
        bodies = [child for child in value if isinstance(child, dict)
                  and str(child.get("type", "")).lower() in {"table_body", "table"}]
        for child in bodies:
            found = _find_html_or_rows(child.get("content", child))
            if found is not None:
                return found
        if any(isinstance(child, dict) and ("type" in child or "content" in child)
               for child in value):
            for child in value:
                if isinstance(child, dict) and str(child.get("type", "")).lower() in {
                    "table_caption", "caption", "table_footnote", "footnote", "footnotes"
                }:
                    continue
                found = _find_html_or_rows(child)
                if found is not None:
                    return found
        return value
    return value


def _contains_table(value: Any) -> bool:
    return isinstance(value, str) and ("<table" in value.lower() or "<tr" in value.lower())


def _has_rows(value: Any) -> bool:
    return isinstance(value, dict) and (isinstance(value.get("rows"), list) or isinstance(value.get("cells"), list))


class _TableHTML(HTMLParser):
    def __init__(self, bbox_scale: Any = None) -> None:
        super().__init__(convert_charrefs=True)
        self.bbox_scale = bbox_scale
        self.rows: list[list[dict[str, Any]]] = []
        self.row: list[dict[str, Any]] | None = None
        self.cell: dict[str, Any] | None = None
        self.text: list[str] = []
        self.footnote_text: list[str] = []
        self.caption: list[str] = []
        self.in_caption = False
        self.footnotes: list[str] = []
        self.footnote_stack: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attr = {k.lower(): v for k, v in attrs}
        if tag == "caption":
            self.in_caption = True
        elif tag in {"td", "th"}:
            if self.row is None:
                self.row = []
            try:
                rowspan = max(1, min(100, int(attr.get("rowspan") or 1)))
                colspan = max(1, min(100, int(attr.get("colspan") or 1)))
            except ValueError:
                rowspan = colspan = 1
            self.cell = {"text": ""}
            if rowspan > 1:
                self.cell["rowspan"] = rowspan
            if colspan > 1:
                self.cell["colspan"] = colspan
            bbox = _box_attr(attr.get("data-bbox") or attr.get("bbox"), self.bbox_scale)
            if bbox:
                self.cell["bbox"] = bbox
            self.text = []
        elif tag in {"sup", "sub", "small", "aside"} or "footnote" in " ".join(attr.get("class", "").split()).lower():
            if tag in {"sup", "sub"} and self.cell is not None:
                self.text.append(f"<{tag}>")
            self.footnote_stack.append(tag)
            self.footnote_text = []
        elif tag in {"br", "wbr"} and self.cell is not None:
            self.text.append(" ")

    def handle_endtag(self, tag: str) -> None:
        if tag in {"td", "th"} and self.cell is not None:
            self.cell["text"] = " ".join("".join(self.text).split())
            assert self.row is not None
            self.row.append(self.cell)
            self.cell = None
            self.text = []
        elif tag == "tr" and self.row is not None:
            self.rows.append(self.row)
            self.row = None
        elif tag == "caption":
            self.in_caption = False
        elif self.footnote_stack and tag == self.footnote_stack[-1]:
            value = " ".join("".join(self.footnote_text).split())
            if value:
                self.footnotes.append(value)
            self.footnote_stack.pop()
            if tag in {"sup", "sub"} and self.cell is not None:
                self.text.append(f"</{tag}>")
            self.footnote_text = []

    def handle_data(self, data: str) -> None:
        if self.cell is not None:
            self.text.append(data)
        if self.footnote_stack:
            self.footnote_text.append(data)
        if self.in_caption:
            self.caption.append(data)


def _parse_table(value: Any, bbox_scale: Any = None) -> tuple[list[list[dict[str, Any]]], str | None]:
    if isinstance(value, dict):
        for html_key in ("html", "table_html", "table_body"):
            if isinstance(value.get(html_key), str):
                return _parse_table(value[html_key], bbox_scale)
        rows = value.get("rows") or value.get("cells")
        if rows is not None:
            return _normalize_rows(rows, bbox_scale), None
        if "content" in value:
            return _parse_table(value["content"], bbox_scale)
    if isinstance(value, list):
        return _normalize_rows(value, bbox_scale), None
    if isinstance(value, str) and _contains_table(value):
        parser = _TableHTML(bbox_scale=bbox_scale)
        try:
            parser.feed(value)
        except Exception as exc:
            raise PDFParserError(f"cannot parse table HTML: {exc}") from exc
        if not parser.rows:
            raise PDFParserError("structured table HTML has no table rows")
        return _expand_spans(parser.rows), value
    return [], None


def _normalize_rows(rows: Any, bbox_scale: Any = None) -> list[list[dict[str, Any]]]:
    if not isinstance(rows, list):
        return []
    out: list[list[dict[str, Any]]] = []
    for row in rows:
        if not isinstance(row, list):
            return []
        normalized = []
        for cell in row:
            if isinstance(cell, dict):
                text = cell.get("text", cell.get("content", ""))
                cell_out: dict[str, Any] = {"text": "" if text is None else str(text)}
                rs, cs = _span(cell.get("rowspan", 1)), _span(cell.get("colspan", 1))
                if rs > 1:
                    cell_out["rowspan"] = rs
                if cs > 1:
                    cell_out["colspan"] = cs
                bbox = _box(cell.get("bbox"), scale=bbox_scale)
                if bbox:
                    cell_out["bbox"] = bbox
                normalized.append(cell_out)
            else:
                normalized.append({"text": "" if cell is None else str(cell)})
        out.append(normalized)
    return _expand_spans(out)


def _expand_spans(rows: list[list[dict[str, Any]]]) -> list[list[dict[str, Any]]]:
    """Return a rectangular grid; covered slots are explicit blank cells.

    The anchor keeps its rowspan/colspan. Placeholder cells never repeat its
    value, which lets downstream code retain both geometry and grid indexes.
    """
    grid: list[list[dict[str, Any]]] = []
    occupied: dict[tuple[int, int], bool] = {}
    max_cols = 0
    for r, row in enumerate(rows):
        out_row: list[dict[str, Any]] = []
        c = 0
        for cell in row:
            while occupied.get((r, c)):
                out_row.append({"text": ""})
                c += 1
            rs, cs = _span(cell.get("rowspan", 1)), _span(cell.get("colspan", 1))
            out_row.append(cell)
            for rr in range(r, min(r + rs, len(rows))):
                for cc in range(c, c + cs):
                    if rr != r or cc != c:
                        occupied[(rr, cc)] = True
            c += 1
            while occupied.get((r, c)):
                out_row.append({"text": ""})
                c += 1
        while occupied.get((r, c)):
            out_row.append({"text": ""})
            c += 1
        grid.append(out_row)
        max_cols = max(max_cols, len(out_row))
    for row in grid:
        row.extend({"text": ""} for _ in range(max_cols - len(row)))
    return grid


def _fragment(page: int | None, bbox: list[float] | None, rows: list[list[dict[str, Any]]],
              source_html: str | None, fragment_id: Any = None) -> dict[str, Any]:
    fid = _safe_id(fragment_id) if fragment_id else f"table-{len(rows):04d}"
    fragment: dict[str, Any] = {"id": fid, "page": page, "bbox": bbox,
                                "bbox_precision": "table" if bbox else None,
                                "caption": None, "footnotes": [], "rows": rows}
    if source_html is not None:
        parser = _TableHTML()
        parser.feed(source_html)
        caption = " ".join("".join(parser.caption).split())
        if caption:
            fragment["caption"] = caption
        fragment["footnotes"] = parser.footnotes
        fragment["raw_html"] = source_html
    return fragment


def _mark_ambiguous_cross_page(
    fragments: list[dict[str, Any]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    # We only construct fragments from page-scoped layout blocks. This guard is
    # for future adapters which may return a global merged table without pages.
    anchored, ambiguous = [], []
    for fragment in fragments:
        if fragment.get("page") is None:
            fragment["requires_review"] = True
            fragment["ambiguity"] = "table has no parser-supplied page provenance; cross-page merge is not trusted"
            ambiguous.append(fragment)
        else:
            anchored.append(fragment)
    return anchored, ambiguous


def _formula_objects(value: Any, page_idx: Any, bbox: Any) -> list[dict[str, Any]]:
    if not isinstance(value, (dict, list)):
        return []
    candidates = value if isinstance(value, list) else [value]
    out: list[dict[str, Any]] = []
    for item in candidates:
        if not isinstance(item, dict):
            continue
        f = _formula(item, int(page_idx) + 1 if isinstance(page_idx, int) and page_idx >= 0 else None,
                     item.get("bbox", bbox))
        if f:
            out.append(f)
    return out


def _raw_text(value: Any) -> str | None:
    if isinstance(value, str):
        return value.strip() or None
    if isinstance(value, list):
        values = [text for item in value if (text := _raw_text(item))]
        return " ".join(values) or None
    if isinstance(value, dict):
        text = value.get("text") or value.get("content")
        return _raw_text(text) if text is not None else None
    return None


def _raw_text_list(value: Any) -> list[str]:
    if value is None:
        return []
    values = value if isinstance(value, list) else [value]
    out = []
    for item in values:
        text = _raw_text(item)
        if text:
            out.append(text)
    return out


def _layout_annotations(value: Any, types: set[str]) -> list[str]:
    if isinstance(value, list):
        out: list[str] = []
        for item in value:
            out.extend(_layout_annotations(item, types))
        return out
    if isinstance(value, dict):
        out = []
        if str(value.get("type", "")).lower() in types:
            text = _raw_text(value.get("content") or value.get("text"))
            if text:
                out.append(text)
        for key, child in value.items():
            if key in {"content", "captions", "footnotes"}:
                out.extend(_layout_annotations(child, types))
        return out
    return []


def _layout_annotation(value: Any, types: set[str]) -> str | None:
    values = _layout_annotations(value, types)
    return " ".join(v for v in values if v) or None


def _formula(value: Mapping[str, Any], page: int | None, bbox: Any,
             width: Any = None, height: Any = None) -> dict[str, Any] | None:
    latex = value.get("latex") or value.get("formula") or value.get("text") or value.get("block_content")
    if not isinstance(latex, str) or not latex.strip():
        return None
    box = _box(bbox, scale=(width, height) if width and height else None)
    return {"latex": latex.strip(), "page": page, "bbox": box}


def _box(value: Any, scale: Any = None) -> list[float] | None:
    if not isinstance(value, (list, tuple)) or len(value) != 4:
        return None
    try:
        coords = [float(v) for v in value]
    except (TypeError, ValueError):
        return None
    if scale == 1000.0:
        coords = [v / 1000.0 for v in coords]
    elif isinstance(scale, tuple) and len(scale) == 2:
        try:
            w, h = float(scale[0]), float(scale[1])
            if w <= 0 or h <= 0:
                return None
            coords = [coords[0] / w, coords[1] / h, coords[2] / w, coords[3] / h]
        except (TypeError, ValueError):
            return None
    elif any(v < 0 or v > 1 for v in coords):
        # Unknown coordinate systems are not silently treated as normalized.
        return None
    if coords[2] <= coords[0] or coords[3] <= coords[1]:
        return None
    if any(v < 0 or v > 1 for v in coords):
        return None
    return [round(v, 6) for v in coords]


def _box_attr(value: Any, scale: Any = None) -> list[float] | None:
    if not isinstance(value, str):
        return None
    try:
        data = json.loads(value)
    except json.JSONDecodeError:
        return None
    return _box(data, scale=scale)


def _span(value: Any) -> int:
    try:
        return min(100, max(1, int(value)))
    except (TypeError, ValueError):
        return 1


def _safe_id(value: Any) -> str:
    text = _SAFE_ID.sub("-", str(value)).strip("-.")[:160]
    if not text or not text[0].isalnum():
        text = "table-" + text
    return text[:160]


def _source_id(value: Any) -> str | None:
    if isinstance(value, (str, int)) and str(value).strip():
        return str(value).strip()[:128]
    return None


def _safe_filename(value: Any) -> str:
    text = str(value).replace("\\", "/").split("/")[-1]
    text = "".join(ch for ch in text if ch not in "\r\n\0").strip()
    return text[:255] or "document.pdf"


def _read_mineru_zip(data: bytes, limit: int) -> Any:
    try:
        archive = zipfile.ZipFile(io.BytesIO(data))
    except (zipfile.BadZipFile, OSError) as exc:
        raise PDFParserError("MinerU artifact is not a valid ZIP") from exc
    infos = archive.infolist()
    if len(infos) > 5000 or sum(x.file_size for x in infos) > limit:
        raise PDFParserError("MinerU ZIP expands beyond configured limit")
    for info in infos:
        # Never extract. Reject suspicious paths to avoid accepting unsafe provenance.
        path = info.filename.replace("\\", "/")
        if path.startswith("/") or any(part in {"..", ""} for part in path.split("/") if part != ""):
            raise PDFParserError("MinerU ZIP contains an unsafe path")
    candidates = sorted((i for i in infos if i.filename.lower().endswith(("middle_json.json", "content_list.json"))),
                        key=lambda i: (not i.filename.lower().endswith("middle_json.json"), i.filename.count("/"), i.filename))
    for info in candidates:
        if info.file_size > limit:
            raise PDFParserError("MinerU structured layout exceeds configured limit")
        try:
            value = json.loads(archive.read(info))
        except (json.JSONDecodeError, OSError):
            continue
        if isinstance(value, dict) and value.get("schema") == "docvortex.middle" and value.get("schema_version") == "2.0":
            return value
        if isinstance(value, list):
            return value
    raise PDFParserError("MinerU ZIP contains no supported structured layout JSON")


def _zip_markdown(data: bytes, limit: int) -> str:
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        candidates = sorted((i for i in archive.infolist() if i.filename.lower().endswith(".md")),
                            key=lambda i: (i.filename.count("/"), i.filename))
        if not candidates:
            return ""
        if candidates[0].file_size > limit:
            raise PDFParserError("MinerU Markdown exceeds configured limit")
        return archive.read(candidates[0]).decode("utf-8", errors="replace")


def _json_safe_artifact(value: Any, limit: int) -> Any:
    encoded = json.dumps(value, ensure_ascii=False, separators=(",", ":"), default=str).encode()
    if len(encoded) > limit:
        raise PDFParserError("retained parser artifact exceeds configured response limit")
    return value


def _env_int(name: str, default: int) -> int:
    try:
        return int(os.getenv(name, str(default)))
    except ValueError:
        return default


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.getenv(name, str(default)))
    except ValueError:
        return default


def _bounded_int(value: Any, lower: int, upper: int, name: str) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"{name} must be an integer") from exc
    return min(upper, max(lower, parsed))
