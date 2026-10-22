import asyncio
import hashlib
import json
from pathlib import Path

import pytest

from structured_data import pdf_parser_bridge as bridge


def test_expand_html_table_preserves_merged_headers_and_true_blank_cells():
    html = (
        '<table><caption>Quarterly results</caption><tr><th rowspan="2">Region</th>'
        '<th colspan="2">Revenue</th></tr><tr><th>Q1</th><th>Q2</th></tr>'
        '<tr><td>North</td><td></td><td>12</td></tr></table>'
    )
    grid, raw_html = bridge._parse_table(html)
    assert raw_html == html
    assert grid == [
        [{"text": "Region", "rowspan": 2}, {"text": "Revenue", "colspan": 2}, {"text": ""}],
        [{"text": ""}, {"text": "Q1"}, {"text": "Q2"}],
        [{"text": "North"}, {"text": ""}, {"text": "12"}],
    ]
    fragment = bridge._fragment(1, [0, 0, 0.5, 0.4], grid, raw_html, "table-a")
    assert fragment["caption"] == "Quarterly results"


def test_superscript_keeps_cell_text_and_retains_marker():
    grid, _ = bridge._parse_table('<table><tr><td>Revenue<sup>1</sup></td></tr></table>')
    assert grid == [[{"text": "Revenue<sup>1</sup>"}]]
    exponent, _ = bridge._parse_table('<table><tr><td>10<sup>2</sup></td></tr></table>')
    assert exponent[0][0]["text"] == "10<sup>2</sup>"
    linebreak, _ = bridge._parse_table('<table><tr><td>1<br>2</td></tr></table>')
    assert linebreak[0][0]["text"] == "1 2"


def test_paddle_per_page_provenance_bbox_and_formula_are_retained():
    pages = [{
        "prunedResult": {
            "width": 1000,
            "height": 2000,
            "parsing_res_list": [
                {"block_label": "table", "block_bbox": [100, 200, 900, 1600],
                 "block_content": '<table><tr><td>A</td><td></td></tr></table>'},
                {"block_label": "formula", "block_bbox": [10, 20, 30, 40],
                 "block_content": r"$x^2$"},
            ],
        }
    }]
    fragments, formulas = bridge._paddle_tables(pages)
    assert len(fragments) == 1
    assert fragments[0]["page"] == 1
    assert fragments[0]["bbox"] == [0.1, 0.1, 0.9, 0.8]
    assert fragments[0]["rows"] == [[{"text": "A"}, {"text": ""}]]
    assert formulas == [{"latex": r"$x^2$", "page": 1, "bbox": [0.01, 0.01, 0.03, 0.02]}]


def test_mineru_middle_table_uses_explicit_page_bbox_and_formula():
    layout = {
        "schema": "docvortex.middle", "schema_version": "2.0",
        "pages": [{"page_idx": 3, "blocks": [
            {"type": "table", "bbox": [0.1, 0.2, 0.8, 0.6],
             "content": {"html": '<table><tr><td>left</td><td>right</td></tr></table>'}},
            {"type": "formula", "bbox": [0.2, 0.7, 0.5, 0.8], "content": {"latex": r"\frac{a}{b}"}},
        ]}],
    }
    fragments, formulas = bridge._mineru_tables(layout)
    assert fragments[0]["page"] == 4
    assert fragments[0]["bbox"] == [0.1, 0.2, 0.8, 0.6]
    assert fragments[0]["bbox_precision"] == "table"
    assert formulas == [{"latex": r"\frac{a}{b}", "page": 4, "bbox": [0.2, 0.7, 0.5, 0.8]}]


def test_mineru_middle_table_body_list_keeps_html_cells():
    layout = {
        "schema": "docvortex.middle", "schema_version": "2.0",
        "pages": [{"page_idx": 0, "blocks": [{"type": "table", "bbox": [0.1, 0.2, 0.8, 0.6],
            "content": [{"type": "table_body", "content": '<table><tr><td>one</td><td></td></tr></table>'}]}]}],
    }
    fragments, _ = bridge._mineru_tables(layout)
    assert fragments[0]["rows"] == [[{"text": "one"}, {"text": ""}]]


def test_mineru_middle_table_selects_body_after_caption_and_keeps_caption():
    layout = {
        "schema": "docvortex.middle", "schema_version": "2.0",
        "pages": [{"page_idx": 1, "blocks": [{"type": "table", "bbox": [0.1, 0.2, 0.8, 0.6],
            "content": [
                {"type": "table_caption", "content": [{"type": "text", "content": "Table A continued"}]},
                {"type": "table_body", "bbox": [0.1, 0.25, 0.8, 0.6],
                 "content": "<table><tr><td>Gamma</td><td>40.00</td><td>50.00</td></tr></table>"},
            ]}]}],
    }
    fragments, _ = bridge._mineru_tables(layout)
    assert len(fragments) == 1
    assert fragments[0]["page"] == 2
    assert fragments[0]["caption"] == "Table A continued"
    assert fragments[0]["rows"] == [[{"text": "Gamma"}, {"text": "40.00"}, {"text": "50.00"}]]


def test_markdown_only_or_absent_structured_fields_fail_explicitly(tmp_path, monkeypatch):
    pdf = tmp_path / "doc.pdf"
    pdf.write_bytes(b"%PDF-1.7\nfixture")
    cfg = bridge.ParserConfig("paddleocr_vl", "http://127.0.0.1:8080")
    monkeypatch.setattr(bridge, "_paddle_layout", lambda content, config: {
        "result": {"layoutParsingResults": []}
    })
    with pytest.raises(bridge.PDFParserError, match="missing result.layoutParsingResults"):
        # Unexpected nesting is not silently treated as layout structures.
        asyncio.run(bridge.parse_pdf(pdf, {}, cfg))

    monkeypatch.setattr(bridge, "_paddle_layout", lambda content, config: {
        "result": {"layoutParsingResults": [{"markdown": {"text": "| A | B |\n|---|---|"},
                                               "prunedResult": {"width": 100, "height": 200, "parsing_res_list": []}}]}
    })
    with pytest.raises(bridge.PDFParserError, match="no structured table cells"):
        asyncio.run(bridge.parse_pdf(pdf, {}, cfg))


def test_unknown_page_provenance_requires_review():
    fragment = bridge._fragment(None, None, [[{"text": "x"}]], None, "merged")
    fragments, ambiguous = bridge._mark_ambiguous_cross_page([fragment])
    assert fragments == []
    assert ambiguous[0]["requires_review"] is True
    assert "page provenance" in ambiguous[0]["ambiguity"]


def test_external_upload_url_is_rejected():
    with pytest.raises(bridge.PDFParserError, match="outside the configured service origin"):
        bridge._trusted_upload_url("http://mineru:8080", "https://files.example/upload")


def test_unanchored_structured_table_is_not_normalized_as_exact_fragment():
    source = {"sha256": "a" * 64, "file_name": "synthetic.pdf"}
    response = {"content_list": [{"type": "table", "table_body": "<table><tr><td>x</td></tr></table>"}]}
    with pytest.raises(bridge.PDFParserError, match="page provenance"):
        bridge.normalize_parser_response("mineru_legacy", response, source)


def test_source_metadata_is_bounded_and_filename_is_path_safe():
    source = {
        "sha256": "a" * 64,
        "file_name": r"..\\private\report\synthetic.pdf",
        "knowledge_id": "knowledge-1",
        "knowledge_base_id": "kb-1",
    }
    response = {"content_list": [{"type": "table", "page_idx": 0,
        "table_body": "<table><tr><td>x</td></tr></table>"}]}
    bundle = bridge.normalize_parser_response("mineru_legacy", response, source)
    assert bundle["source"] == {
        "sha256": "a" * 64,
        "file_name": "synthetic.pdf",
        "knowledge_id": "knowledge-1",
        "knowledge_base_id": "kb-1",
    }
    assert bridge._safe_filename("folder/evil\r\n.pdf") == "evil.pdf"


def test_partial_unanchored_table_is_preserved_as_ambiguity_sidecar():
    source = {"sha256": "a" * 64, "file_name": "synthetic.pdf"}
    response = {"content_list": [
        {"type": "table", "page_idx": 0, "bbox": [0, 0, 1000, 1000],
         "table_body": "<table><tr><td>page one</td></tr></table>"},
        {"type": "table", "table_body": "<table><tr><td>merged unknown</td></tr></table>"},
    ]}
    bundle = bridge.normalize_parser_response("mineru_legacy", response, source)
    assert [f["page"] for f in bundle["fragments"]] == [1]
    assert bundle["requires_review"] is True
    assert bundle["completeness"] == "unknown"
    assert bundle["ambiguities"][0]["rows"][0][0]["text"] == "merged unknown"
    assert bundle["raw"]["structured"] == response["content_list"]


def test_source_sha_is_computed_and_mismatch_is_rejected(tmp_path, monkeypatch):
    pdf = tmp_path / "doc.pdf"
    content = b"%PDF-1.7\nfixture"
    pdf.write_bytes(content)
    cfg = bridge.ParserConfig("paddleocr_vl", "http://127.0.0.1:8080")
    monkeypatch.setattr(bridge, "_paddle_layout", lambda *_: {"result": {"layoutParsingResults": []}})
    with pytest.raises(bridge.PDFParserError, match="source sha256"):
        asyncio.run(bridge.parse_pdf(pdf, {"sha256": "0" * 64}, cfg))
    assert hashlib.sha256(content).hexdigest() != "0" * 64

