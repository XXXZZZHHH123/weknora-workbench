import io
import json
import logging

from markitdown import MarkItDown

from docreader.config import CONFIG
from docreader.models.document import Document
from docreader.parser.base_parser import BaseParser
from docreader.parser.chain_parser import PipelineParser
from docreader.parser.concurrency import parser_worker_limit
from docreader.parser.docx_merge import fill_vertical_merged_cells_docx
from docreader.parser.markdown_parser import MarkdownParser
from docreader.parser.ppt_convert import normalize_ppt_bytes
from docreader.parser.pptx_media import (
    attach_pptx_media_to_markdown,
)
from docreader.parser.xlsx_repair import (
    sanitize_xlsx_styles,
    strip_unreadable_ranges_xlsx,
)

logger = logging.getLogger(__name__)


class StdMarkitdownParser(BaseParser):
    """
    Standard MarkItDown Parser Wrapper

    This parser uses the markitdown library to convert various document formats
    (docx, pptx, pdf, etc.) into text/markdown.
    """

    def __init__(self, *args, **kwargs):
        # 这里的 super() 会调用 BaseParser 的初始化，确保 self.file_type 被正确赋值
        super().__init__(*args, **kwargs)
        self.markitdown = MarkItDown()

    def parse_into_text(self, content: bytes) -> Document:
        """
        Parses content using MarkItDown.
        Uses self.file_type (inherited from BaseParser) to hint the stream format.
        """
        ext = self.file_type
        ft = (ext or "").lstrip(".").lower()
        pptx_bytes: bytes | None = None
        if ft in ("ppt", "pptx"):
            content, ext = normalize_ppt_bytes(content, ft)
            pptx_bytes = content
            ft = "pptx"
        elif ft == "docx":
            content = fill_vertical_merged_cells_docx(content)
        elif ft in ("xlsx", "xlsm"):
            # MarkItDown reads spreadsheets through pandas/openpyxl, so the
            # ranges openpyxl cannot parse (#3599) and the non-conforming
            # styles.xml fills (#3637) that break the builtin ExcelParser break
            # this engine too. Strip the ranges first: the fill repair alone
            # would still leave the range TypeError. Both passes return None
            # for clean workbooks.
            readable = strip_unreadable_ranges_xlsx(content)
            if readable is not None:
                content = readable
            sanitized = sanitize_xlsx_styles(content)
            if sanitized is not None:
                content = sanitized
        if ext and not ext.startswith("."):
            ext = "." + ext

        with parser_worker_limit("markitdown", CONFIG.markitdown_max_workers):
            # PPTX pictures are resolved from exact slide relationships below.
            # Letting MarkItDown inline them first needlessly builds/copies large
            # base64 strings, especially when a repeated logo appears per page.
            result = self._convert_markitdown(content, ext, keep_data_uris=pptx_bytes is None)
            if result is None:
                logger.warning(
                    "MarkItDown failed with embedded images for %s; retrying without data URIs",
                    ft or ext,
                )
                result = self._convert_markitdown(content, ext, keep_data_uris=False)

            # Raster conversion belongs to the same heavy parser slot. Without
            # this, concurrent uploads can launch separate LibreOffice batches
            # after MarkItDown has released the configured worker semaphore.
            text = result.text_content
            images: dict[str, str] = {}
            image_sources: list[dict] = []
            if pptx_bytes is not None:
                text, images = attach_pptx_media_to_markdown(text, pptx_bytes, image_sources=image_sources)
        metadata = {}
        if image_sources:
            metadata["pptx_image_sources_v1"] = json.dumps(image_sources, ensure_ascii=False)
        return Document(content=text, images=images, metadata=metadata)

    def _convert_markitdown(
        self, content: bytes, ext: str | None, *, keep_data_uris: bool
    ):
        try:
            return self.markitdown.convert(
                io.BytesIO(content),
                file_extension=ext,
                keep_data_uris=keep_data_uris,
            )
        except Exception:
            if keep_data_uris:
                return None
            raise


class MarkitdownParser(PipelineParser):
    _parser_cls = (StdMarkitdownParser, MarkdownParser)
