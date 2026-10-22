"""Render one explicitly selected PPTX EMF/WMF source media part as PNG."""

from __future__ import annotations

import base64
from collections import OrderedDict
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
import logging
import hashlib
import io
import posixpath
import re
import threading
import time
import unicodedata
import zipfile

from docreader.models.document import Document
from docreader.parser.pptx_media import (
    _read_member,
    _slide_parts,
    _slide_pictures,
    rasterize_media_bytes,
    conversion_cancellation,
)

SOURCE_MEDIA_PNG_ENGINE = "source_media_png_v1"
_MAX_PRESENTATION_BYTES = 32 * 1024 * 1024
_MAX_MEDIA_BYTES = 16 * 1024 * 1024
_MAX_PNG_BYTES = 8 * 1024 * 1024
_MAX_PIXELS = 40_000_000
_MAX_DIMENSION = 12_000
_RENDER_TIMEOUT_SECONDS = 30.0
_MAX_CONCURRENT_RENDERS = 2
_RENDER_SLOTS = threading.BoundedSemaphore(_MAX_CONCURRENT_RENDERS)
_QUEUE_TIMEOUT_SECONDS = 10.0
_REQUEST_TIMEOUT_SECONDS = _QUEUE_TIMEOUT_SECONDS + _RENDER_TIMEOUT_SECONDS + 5.0
_CACHE_TTL_SECONDS = 600.0
_CACHE_MAX_BYTES = 64 * 1024 * 1024
_CACHE_MAX_ITEMS = 128
_MAX_PENDING_RENDERS = 16
_RENDER_VERSION = "standalone-office-png-v1"
_STATE_LOCK = threading.Lock()
_CACHE = OrderedDict()
_CACHE_BYTES = 0
_FLIGHTS = {}
_REQUEST_CONTEXT = ContextVar("source_media_request_context", default=None)
logger = logging.getLogger(__name__)
_MEDIA_PART_PREFIX = "ppt/media/"


class SourceMediaSelectorError(ValueError):
    """A caller selected a page or media part outside the stored source."""


class SourceMediaRenderError(ValueError):
    """Public, bounded operational status without converter/source payloads."""

    def __init__(self, code):
        self.code = code
        messages = {
            "busy": "Source media renderer capacity is busy",
            "timeout": "Source media renderer timed out",
            "cancelled": "Source media render request was cancelled",
            "render_failed": "Selected source media could not be rendered",
        }
        super().__init__(messages[code])


@contextmanager
def source_media_request_context(context):
    token = _REQUEST_CONTEXT.set(context)
    try:
        yield
    finally:
        _REQUEST_CONTEXT.reset(token)


def _request_cancelled():
    context = _REQUEST_CONTEXT.get()
    return context is not None and not context.is_active()


@dataclass
class _RenderFlight:
    done: threading.Event = field(default_factory=threading.Event)
    cancel: threading.Event = field(default_factory=threading.Event)
    waiters: int = 0
    peak_waiters: int = 0
    png: bytes | None = None
    error: SourceMediaRenderError | None = None


def _cached_png(key):
    """Read the process-local LRU while holding _STATE_LOCK."""
    global _CACHE_BYTES
    now = time.monotonic()
    for stale in [k for k, (expiry, _) in _CACHE.items() if expiry <= now]:
        _CACHE_BYTES -= len(_CACHE.pop(stale)[1])
    entry = _CACHE.get(key)
    if entry:
        _CACHE.move_to_end(key)
        return entry[1]
    return None


def _save_png(key, png):
    """Store validated immutable PNG bytes while holding _STATE_LOCK."""
    global _CACHE_BYTES
    if len(png) > _CACHE_MAX_BYTES:
        return
    previous = _CACHE.pop(key, None)
    if previous:
        _CACHE_BYTES -= len(previous[1])
    _CACHE[key] = (time.monotonic() + _CACHE_TTL_SECONDS, png)
    _CACHE_BYTES += len(png)
    while _CACHE and (len(_CACHE) > _CACHE_MAX_ITEMS or _CACHE_BYTES > _CACHE_MAX_BYTES):
        _, (_, evicted) = _CACHE.popitem(last=False)
        _CACHE_BYTES -= len(evicted)


def _render_flight(key, flight, part, raw_media):
    acquired = False
    queued = time.monotonic()
    try:
        # Waiting has an independent, bounded capacity budget. Time spent here
        # never shortens an acquired converter's full 30-second execution budget.
        while not flight.cancel.is_set():
            remaining = _QUEUE_TIMEOUT_SECONDS - (time.monotonic() - queued)
            if remaining <= 0:
                raise SourceMediaRenderError("busy")
            acquired = _RENDER_SLOTS.acquire(timeout=min(0.1, remaining))
            if acquired:
                break
        if flight.cancel.is_set():
            raise SourceMediaRenderError("cancelled")
        started = time.monotonic()
        with conversion_cancellation(flight.cancel.is_set):
            png = rasterize_media_bytes(part, raw_media, timeout=_RENDER_TIMEOUT_SECONDS)
        if flight.cancel.is_set():
            raise SourceMediaRenderError("cancelled")
        if time.monotonic() - started >= _RENDER_TIMEOUT_SECONDS:
            raise SourceMediaRenderError("timeout")
        if png is None:
            raise SourceMediaRenderError("render_failed")
        _validate_png(png)
        with _STATE_LOCK:
            flight.png = png
            _save_png(key, png)
        logger.info("Source media render completed queue_ms=%d render_ms=%d shared_waiters=%d",
                    round((started-queued)*1000), round((time.monotonic()-started)*1000), flight.peak_waiters)
    except SourceMediaRenderError as exc:
        flight.error = exc
        logger.info("Source media render ended status=%s elapsed_ms=%d", exc.code,
                    round((time.monotonic() - queued) * 1000))
    except Exception:
        flight.error = SourceMediaRenderError("render_failed")
        logger.warning("Source media render ended status=render_failed")
    finally:
        if acquired:
            _RENDER_SLOTS.release()
        with _STATE_LOCK:
            if _FLIGHTS.get(key) is flight:
                del _FLIGHTS[key]
            flight.done.set()


def _render_shared(key, part, raw_media):
    if _request_cancelled():
        raise SourceMediaRenderError("cancelled")
    with _STATE_LOCK:
        cached = _cached_png(key)
        if cached is not None:
            logger.info("Source media render cache_hit")
            return cached
        flight = _FLIGHTS.get(key)
        if flight is None:
            if len(_FLIGHTS) >= _MAX_PENDING_RENDERS:
                raise SourceMediaRenderError("busy")
            flight = _RenderFlight()
            _FLIGHTS[key] = flight
            worker = threading.Thread(target=_render_flight, args=(key, flight, part, raw_media), daemon=True)
        else:
            if flight.cancel.is_set():
                raise SourceMediaRenderError("busy")
            worker = None
        flight.waiters += 1
        flight.peak_waiters = max(flight.peak_waiters, flight.waiters)
    deadline = time.monotonic() + _REQUEST_TIMEOUT_SECONDS
    try:
        if worker is not None:
            try:
                worker.start()
            except RuntimeError:
                with _STATE_LOCK:
                    _FLIGHTS.pop(key, None)
                    flight.error = SourceMediaRenderError("busy")
                    flight.done.set()
        while not flight.done.wait(timeout=0.05):
            if _request_cancelled():
                raise SourceMediaRenderError("cancelled")
            if time.monotonic() >= deadline:
                raise SourceMediaRenderError("timeout")
        if _request_cancelled():
            raise SourceMediaRenderError("cancelled")
        if flight.error is not None:
            raise flight.error
        return flight.png
    finally:
        with _STATE_LOCK:
            flight.waiters -= 1
            if flight.waiters == 0 and not flight.done.is_set():
                flight.cancel.set()


def _selected_page(options: dict) -> int:
    value = options.get("source_page")
    if isinstance(value, bool) or not isinstance(value, (str, int)):
        raise SourceMediaSelectorError("A positive source_page is required")
    text = str(value)
    if not re.fullmatch(r"[0-9]{1,4}", text):
        raise SourceMediaSelectorError("A positive source_page is required")
    page = int(text)
    if page < 1:
        raise SourceMediaSelectorError("A positive source_page is required")
    return page


def _selected_part(options: dict) -> str:
    part = options.get("source_part")
    if not isinstance(part, str) or not part.startswith(_MEDIA_PART_PREFIX):
        raise SourceMediaSelectorError("A PPTX EMF/WMF source_part is required")
    try:
        encoded = part.encode("utf-8")
    except UnicodeEncodeError as exc:
        raise SourceMediaSelectorError("Invalid source_part") from exc
    basename = part[len(_MEDIA_PART_PREFIX) :]
    if (
        len(encoded) > 1024
        or not basename
        or "/" in basename
        or "\\" in basename
        or any(unicodedata.category(char) == "Cc" for char in basename)
        or posixpath.normpath(part) != part
    ):
        raise SourceMediaSelectorError("Invalid source_part")
    stem, separator, extension = basename.rpartition(".")
    if (
        not separator
        or extension.lower() not in {"emf", "wmf"}
        or not stem.strip()
        or stem in {".", ".."}
    ):
        raise SourceMediaSelectorError("A PPTX EMF/WMF source_part is required")
    return part


def _validate_png(data: bytes) -> tuple[int, int]:
    if not isinstance(data, bytes) or not data or len(data) > _MAX_PNG_BYTES:
        raise ValueError("Rendered PNG is empty or exceeds the 8 MiB limit")
    if not data.startswith(b"\x89PNG\r\n\x1a\n"):
        raise ValueError("Selected source media did not render as PNG")
    try:
        from PIL import Image

        with Image.open(io.BytesIO(data)) as image:
            if image.format != "PNG":
                raise ValueError("Selected source media did not render as PNG")
            width, height = image.size
            if (
                width < 1
                or height < 1
                or width > _MAX_DIMENSION
                or height > _MAX_DIMENSION
                or width * height > _MAX_PIXELS
            ):
                raise ValueError("Rendered PNG dimensions exceed the supported limits")
            image.verify()
    except ValueError:
        raise
    except Exception as exc:
        raise ValueError("Rendered PNG is invalid") from exc
    return width, height


def read_source_media_png(content: bytes, file_type: str, options: dict) -> Document:
    """Read and render exactly one relationship-bound vector media member.

    The input is the stored PPTX byte sequence. URLs and package-wide rendering
    are deliberately outside this engine's contract.
    """
    if file_type.lower().lstrip(".") != "pptx":
        raise ValueError("Source media PNG rendering requires a PPTX file")
    if not isinstance(content, bytes) or not 0 < len(content) <= _MAX_PRESENTATION_BYTES:
        raise ValueError("Stored PPTX bytes are empty or exceed the 32 MiB limit")
    if not isinstance(options, dict):
        raise ValueError("Source media PNG options are invalid")

    page_number = _selected_page(options)
    part = _selected_part(options)
    if _request_cancelled():
        raise SourceMediaRenderError("cancelled")
    try:
        with zipfile.ZipFile(io.BytesIO(content)) as archive:
            pages = _slide_parts(archive)
            if not 1 <= page_number <= len(pages):
                raise SourceMediaSelectorError("source_page is outside the presentation")
            names = archive.namelist()
            if names.count(part) != 1:
                raise SourceMediaSelectorError("source_part is missing or ambiguous in the PPTX")
            related_parts = {target for _, target, _ in _slide_pictures(archive, pages[page_number - 1])}
            if part not in related_parts:
                raise SourceMediaSelectorError("source_part is not related to source_page")
            raw_media = _read_member(archive, part, _MAX_MEDIA_BYTES)
    except ValueError:
        raise
    except (zipfile.BadZipFile, KeyError, OSError, RuntimeError) as exc:
        raise ValueError("Stored PPTX relationships could not be read") from exc

    source_sha = hashlib.sha256(content).hexdigest()
    media_sha = hashlib.sha256(raw_media).hexdigest()
    # Cache only immutable successful PNG bytes. The source/page/part checks
    # above run on every request, including cache hits and shared-flight joins.
    key = (source_sha, part, media_sha, _RENDER_VERSION)
    png = _render_shared(key, part, raw_media)
    metadata = {
        "evidence_version": SOURCE_MEDIA_PNG_ENGINE,
        "source_file_sha256": source_sha,
        "source_media_sha256": media_sha,
        "source_page": str(page_number),
        "source_part": part,
        "rendered_png_sha256": hashlib.sha256(png).hexdigest(),
        "rendered_png_base64": base64.b64encode(png).decode("ascii"),
    }
    return Document(content="Rendered the selected original PPTX media part as PNG.", metadata=metadata)
