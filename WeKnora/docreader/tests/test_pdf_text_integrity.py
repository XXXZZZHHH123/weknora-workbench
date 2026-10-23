"""Regression coverage for preserving ambiguous PDF text-layer content."""

import unittest

from docreader.parser.pdf_parser import PDFParser


def _make_text_pdf(pages):
    """Build a small native-text PDF without external fixture tooling.

    Each page is a sequence of (text, x, y, font_size) tuples.  The fixture is
    intentionally simple: it exercises parser preservation, not table recovery.
    """
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        None,  # Pages tree is filled after page object numbers are known.
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    page_ids = []
    for lines in pages:
        ops = []
        for text, x, y, size in lines:
            escaped = text.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")
            ops.append(f"BT /F1 {size} Tf {x} {y} Td ({escaped}) Tj ET")
        stream = "\n".join(ops).encode("ascii")
        page_id = len(objects) + 1
        stream_id = page_id + 1
        page_ids.append(page_id)
        objects.append(
            f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] "
            f"/Resources << /Font << /F1 3 0 R >> >> /Contents {stream_id} 0 R >>".encode()
        )
        objects.append(
            b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n"
            + stream + b"\nendstream"
        )
    kids = " ".join(f"{page_id} 0 R" for page_id in page_ids)
    objects[1] = f"<< /Type /Pages /Kids [{kids}] /Count {len(page_ids)} >>".encode()

    pdf = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for object_id, body in enumerate(objects, 1):
        offsets.append(len(pdf))
        pdf.extend(f"{object_id} 0 obj\n".encode() + body + b"\nendobj\n")
    xref = len(pdf)
    pdf.extend(f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode())
    for offset in offsets[1:]:
        pdf.extend(f"{offset:010d} 00000 n \n".encode())
    pdf.extend(
        f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\n"
        f"startxref\n{xref}\n%%EOF\n".encode()
    )
    return bytes(pdf)


def _cell(text, x, y, size=12):
    return (text, x, y, size)


class PdfTextIntegrityTest(unittest.TestCase):
    def assert_source_offsets_match_content(self, document):
        for block in document.source_blocks:
            start, end = block["start"], block["end"]
            self.assertGreaterEqual(start, 0)
            self.assertGreater(end, start)
            self.assertLessEqual(end, len(document.content))
            self.assertTrue(document.content[start:end].strip())

    def page_for_text_offset(self, document, value):
        offset = document.content.index(value)
        for block in document.source_blocks:
            if block["start"] <= offset < block["end"]:
                return block["locator"]["page"]
        self.fail(f"no source block covers {value!r} at offset {offset}")

    def test_single_page_numeric_cells_negative_decimals_and_empty_cell(self):
        pdf = _make_text_pdf(
            [[
                _cell("Year", 55, 670), _cell("North", 205, 670), _cell("South", 355, 670),
                _cell("2023", 55, 640), _cell("123.45", 205, 640), _cell("67.89", 355, 640),
                _cell("2024", 55, 610), _cell("98.76", 355, 610),
                _cell("2025", 55, 580), _cell("-12.50", 205, 580), _cell("0.00", 355, 580),
                _cell("x", 100, 500), _cell("2", 107, 508, 8), _cell("+ y", 120, 500),
            ]]
        )
        document = PDFParser(file_name="numeric.pdf", file_type="pdf").parse_into_text(pdf)
        content = document.content
        self.assert_source_offsets_match_content(document)
        for value in ("2023", "123.45", "67.89", "2024", "98.76", "2025", "-12.50", "0.00", "x", "2", "+ y"):
            self.assertIn(value, content)
        # The absent 2024/North cell stays absent; text parsing does not invent it.
        self.assertNotIn("2024 98.76 0", content)

    def test_two_page_continuation_keeps_every_row_value(self):
        pdf = _make_text_pdf(
            [
                [
                    _cell("Item", 55, 670), _cell("North", 205, 670), _cell("South", 355, 670),
                    _cell("Alpha", 55, 640), _cell("10.25", 205, 640), _cell("20.50", 355, 640),
                    _cell("Beta", 55, 610), _cell("30.75", 205, 610),
                ],
                [
                    _cell("Gamma", 55, 750), _cell("40.00", 205, 750), _cell("50.00", 355, 750),
                    _cell("Delta", 55, 720), _cell("-2.50", 205, 720), _cell("0.00", 355, 720),
                ],
            ]
        )
        document = PDFParser(file_name="continuation.pdf", file_type="pdf").parse_into_text(pdf)
        content = document.content
        self.assert_source_offsets_match_content(document)
        self.assertEqual(self.page_for_text_offset(document, "30.75"), 1)
        self.assertEqual(self.page_for_text_offset(document, "40.00"), 2)
        for value in ("Alpha", "10.25", "20.50", "Beta", "30.75", "Gamma", "40.00", "50.00", "Delta", "-2.50", "0.00"):
            self.assertIn(value, content)


if __name__ == "__main__":
    unittest.main()
