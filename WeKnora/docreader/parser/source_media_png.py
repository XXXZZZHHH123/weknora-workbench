"""Render one explicitly selected PPTX EMF/WMF source media part as PNG."""

from __future__ import annotations

import base64
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
_MEDIA_PART_PREFIX = "ppt/media/"


class SourceMediaSelectorError(ValueError):
    """A caller selected a page or media part outside the stored source."""


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
    started = time.monotonic()
    acquired = _RENDER_SLOTS.acquire(timeout=_RENDER_TIMEOUT_SECONDS)
    if not acquired:
        raise ValueError("Source media PNG render budget expired while waiting for capacity")

    try:
        remaining = _RENDER_TIMEOUT_SECONDS - (time.monotonic() - started)
        if remaining <= 0:
            raise ValueError("Source media PNG render budget expired")
        try:
            with zipfile.ZipFile(io.BytesIO(content)) as archive:
                pages = _slide_parts(archive)
                if not 1 <= page_number <= len(pages):
                    raise SourceMediaSelectorError("source_page is outside the presentation")
                names = archive.namelist()
                if names.count(part) != 1:
                    raise SourceMediaSelectorError("source_part is missing or ambiguous in the PPTX")
                related_parts = {
                    target
                    for _, target, _ in _slide_pictures(archive, pages[page_number - 1])
                }
                if part not in related_parts:
                    raise SourceMediaSelectorError("source_part is not related to source_page")
                raw_media = _read_member(archive, part, _MAX_MEDIA_BYTES)
        except ValueError:
            raise
        except (zipfile.BadZipFile, KeyError, OSError, RuntimeError) as exc:
            raise ValueError("Stored PPTX relationships could not be read") from exc

        remaining = _RENDER_TIMEOUT_SECONDS - (time.monotonic() - started)
        if remaining <= 0:
            raise ValueError("Source media PNG render budget expired")
        try:
            # rasterize_media_bytes applies the same remaining deadline across
            # its LibreOffice and ImageMagick fallbacks and cleans temp dirs.
            png = rasterize_media_bytes(part, raw_media, timeout=remaining)
        except Exception as exc:
            raise ValueError("Selected source media could not be rendered") from exc
        if time.monotonic() - started > _RENDER_TIMEOUT_SECONDS:
            raise ValueError("Source media PNG render budget expired")
        if png is None:
            raise ValueError("Selected source media could not be rendered")
        _validate_png(png)

        metadata = {
            "evidence_version": SOURCE_MEDIA_PNG_ENGINE,
            "source_file_sha256": hashlib.sha256(content).hexdigest(),
            "source_media_sha256": hashlib.sha256(raw_media).hexdigest(),
            "source_page": str(page_number),
            "source_part": part,
            "rendered_png_sha256": hashlib.sha256(png).hexdigest(),
            "rendered_png_base64": base64.b64encode(png).decode("ascii"),
        }
        return Document(
            content="Rendered the selected original PPTX media part as PNG.",
            metadata=metadata,
        )
    finally:
        _RENDER_SLOTS.release()
