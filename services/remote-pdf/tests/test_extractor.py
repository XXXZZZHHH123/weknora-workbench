import asyncio
from copy import deepcopy
import json
from pathlib import Path
import sys

import fitz
import httpx
import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from remote_pdf import extractor as e


def sample(value="142"):
    return {"text": "A source heading", "formulas": [{"latex": "x^2", "text": "x²"}],
            "tables": [{"title": "table", "columns": ["region", "count"], "units": [None, "persons"],
                        "rows": [["North", value], ["South", "18"]], "notes": [], "issues": []}], "issues": []}


def config(**changes):
    return e.ExtractorConfig(endpoint="https://model.invalid/v1", api_key="secret", model="vision", **changes)


def pdf(path, pages=2):
    with fitz.open() as doc:
        for index in range(pages):
            doc.new_page().insert_text((50, 50), "Page " + str(index + 1))
        doc.save(path)


def test_strict_grid_rejects_misplaced_header():
    value = sample()
    value["tables"][0]["columns"].pop()
    with pytest.raises(e.OutputValidationError):
        e.validate_page(value)
    quarantined = e.validate_page(value, quarantine_grid=True)
    assert len(quarantined["tables"][0]["columns"]) == 1
    assert quarantined["tables"][0]["rows"][0] == ["North", "142"]
    assert "SCHEMA_GRID_MISMATCH" in quarantined["tables"][0]["issues"][0]


@pytest.mark.parametrize("value", [142, True, "null", "NaN"])
def test_cell_types_do_not_silently_coerce(value):
    with pytest.raises(e.OutputValidationError):
        e.validate_page(sample(value))


def test_disagreeing_digit_cannot_be_published_or_indexed_as_fact():
    a, b = sample("142"), sample("143")
    selected = e._select([a, b, b])
    tables = e.assess_tables(selected, [a, b, b], True)
    assert tables[0]["quality"]["status"] == "needs_review"
    assert tables[0]["uncertain_cells"] == [[0, 1]]
    page = {**selected, "tables": tables, "page": 1, "quality": {"status": "needs_review"}}
    bundle = e.build_bundle({"sha256": "0" * 64}, [page], {})
    assert bundle["fragments"][0]["rows"][1][1]["uncertain"] is True
    assert "143" not in e.page_markdown(page)
    assert "142" not in e.page_markdown(page)


def test_missing_cell_not_equal_to_blank():
    page = sample(None)
    table = e.assess_tables(page, [page, deepcopy(page)], True)[0]
    assert table["quality"]["status"] == "needs_review"
    assert table["uncertain_cells"] == [[0, 1]]


def test_schema_quarantine_retains_grid_and_marks_generated_slots():
    original = sample()
    original["tables"][0]["columns"] = ["region"]
    page = e.validate_page(original, quarantine_grid=True)
    page["tables"] = e.assess_tables(page, [page, deepcopy(page)], True)
    page.update(page=1, quality={"status": "needs_review"})
    bundle = e.build_bundle({}, [page], {})
    cells = bundle["fragments"][0]["rows"]
    assert cells[0][1]["generated_padding"] is True
    assert cells[1][1]["text"] == "142"
    assert all(c["uncertain"] for row in cells for c in row)
    assert "142" not in e.page_markdown(page)


def test_text_formula_and_units_are_retained():
    page = sample()
    page["text"] = "中" * 20000
    page["tables"] = e.assess_tables(page, [page, deepcopy(page)], True)
    page.update(page=9, quality={"status": "passed"})
    bundle = e.build_bundle({}, [page], {})
    assert "".join(c["text"] for c in bundle["page_context"]) == page["text"]
    assert all(len(c["text"].encode()) < 16384 for c in bundle["page_context"])
    assert bundle["formulas"][0]["page"] == 9
    assert "x^2" in e.page_markdown(page)
    assert "142" in e.page_markdown(page)


def test_profile_isolates_credentials_renderer_and_sampling():
    a = config()
    assert "secret" not in json.dumps(a.profile())
    assert e._hash(a.profile()) != e._hash(config(dpi=200).profile())
    b = e.ExtractorConfig(endpoint=a.endpoint, api_key="different", model=a.model)
    assert e._hash(a.profile()) != e._hash(b.profile())
    assert "renderer_version" in a.profile()


def test_reasoning_configuration_cannot_override_protocol_or_destination():
    a = config(request_options={"thinking": {"type": "disabled"}})
    assert a.profile()["request_options"]["thinking"] == {"type": "disabled"}
    for option in ({"model": "other"}, {"messages": []}, {"stream": True}, {"base_url": "http://other"},
                   {"thinking": {"type": "disabled", "key": "injected"}}):
        with pytest.raises(ValueError):
            config(request_options=option)


def test_relation_rejects_duplicate_or_incomplete_row_alignment():
    left = [{"id": "a", "caption": "Table alpha", "rows": [[{}], [{}], [{}]]}]
    right = [{"id": "b", "caption": "Table alpha continued", "rows": [[{}], [{}], [{}]]}]
    relation = {"left_fragment_id": "a", "right_fragment_id": "b", "kind": "horizontal", "row_pairs": [[1, 1]], "reason": "same rows",
                "same_table_evidence": [{"side": "left", "role": "caption", "quote": "Table alpha"},
                                        {"side": "right", "role": "continuation", "quote": "Table alpha continued"}]}
    with pytest.raises(e.OutputValidationError):
        e.validate_relations({"relations": [relation]}, left, right)
    relation["row_pairs"] = [[1, 1], [2, 1]]
    with pytest.raises(e.OutputValidationError):
        e.validate_relations({"relations": [relation]}, left, right)
    relation["row_pairs"] = [[1, 1], [2, 2]]
    assert e.validate_relations({"relations": [relation]}, left, right)[0]["needs_review"] is True


def test_shared_row_keys_without_table_identity_cannot_assert_continuation():
    left = [{"id": "a", "caption": "Households", "rows": [[{}], [{}]]}]
    right = [{"id": "b", "caption": "Education", "rows": [[{}], [{}]]}]
    relation = {"left_fragment_id": "a", "right_fragment_id": "b", "kind": "horizontal", "row_pairs": [[1, 1]], "reason": "same region key"}
    with pytest.raises(e.OutputValidationError, match="identity evidence"):
        e.validate_relations({"relations": [relation]}, left, right)
    relation["kind"] = "unrelated"
    assert e.validate_relations({"relations": [relation]}, left, right) == []
    relation["kind"] = "horizontal"
    relation["same_table_evidence"] = [{"side": "left", "role": "title", "quote": "invented continuation"}]
    with pytest.raises(e.OutputValidationError, match="not present"):
        e.validate_relations({"relations": [relation]}, left, right)


def test_continuation_repair_uses_source_indices_and_is_bounded(tmp_path):
    a = {"id": "a", "page": 1, "caption": "Table alpha", "rows": [[{}], [{}], [{}]]}
    b = {"id": "b", "page": 2, "caption": "Table alpha continued", "rows": [[{}], [{}], [{}]]}
    class Client:
        def __init__(self, always_invalid=False):
            self.prompts, self.always_invalid = [], always_invalid
        async def request(self, prompt):
            self.prompts.append(prompt)
            pairs = [[1, 1]] if len(self.prompts) == 1 or self.always_invalid else [[1, 1], [2, 2]]
            return {"relations": [{"left_fragment_id": "a", "right_fragment_id": "b", "kind": "horizontal", "row_pairs": pairs,
                                    "reason": "Source continuation caption", "same_table_evidence": [
                                        {"side": "left", "role": "caption", "quote": "Table alpha"},
                                        {"side": "right", "role": "continuation", "quote": "Table alpha continued"}]}]}, {}
    bundle = {"fragments": [a, b], "page_context": []}
    client = Client()
    result, issues = asyncio.run(e._continuations(bundle, tmp_path / "valid", client, None, None))
    assert len(client.prompts) == 2 and not issues
    assert result[0]["row_pairs"] == [[1, 1], [2, 2]]
    assert '"data_row_indices":[1,2]' in client.prompts[1]
    broken = Client(always_invalid=True)
    result, issues = asyncio.run(e._continuations(bundle, tmp_path / "invalid", broken, None, None))
    assert len(broken.prompts) == 2 and result == []
    assert issues[0]["needs_review"] is True


def test_two_pass_checkpoint_resume_skips_paid_calls(tmp_path, monkeypatch):
    calls = []
    async def request(self, prompt, images=()):
        calls.append(prompt)
        return sample(), {"finish_reason": "stop"}
    monkeypatch.setattr(e.ModelClient, "request", request)
    source = tmp_path / "source.pdf"
    pdf(source)
    async def run():
        first = await e.extract_pdf(source, tmp_path / "output", config(detect_continuations=False))
        assert len(calls) == 4
        assert first["quality"]["complete_pages"] == 2
        second = await e.extract_pdf(source, tmp_path / "output", config(detect_continuations=False))
        assert len(calls) == 4
        assert second["quality"]["cached_pages"] == 2
        assert [p["page"] for p in second["pages"]] == [1, 2]
    asyncio.run(run())


def test_cancellation_releases_model_slot_and_no_checkpoint(tmp_path, monkeypatch):
    source = tmp_path / "source.pdf"
    pdf(source, 1)
    async def run():
        slot = asyncio.Semaphore(1)
        entered = asyncio.Event()
        async def request(self, prompt, images=()):
            async with self.semaphore:
                entered.set()
                await asyncio.sleep(60)
        monkeypatch.setattr(e.ModelClient, "request", request)
        task = asyncio.create_task(e.extract_pdf(source, tmp_path / "output", config(), page_semaphore=slot))
        await entered.wait()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert slot._value == 1
        assert not list((tmp_path / "output").rglob("page-*.json"))
    asyncio.run(run())


def test_request_rejects_length_and_never_follows_redirects():
    async def run():
        client = e.ModelClient(config(max_retries=0), asyncio.Semaphore(1))
        await client.client.aclose()
        client.client = httpx.AsyncClient(transport=httpx.MockTransport(lambda request: httpx.Response(200, json={"choices": [{"finish_reason": "length", "message": {"content": "{}"}}]})))
        with pytest.raises(e.OutputValidationError, match="finish normally"):
            await client.request("read")
        await client.close()
    asyncio.run(run())


@pytest.mark.parametrize("status,code", [(401, "MODEL_AUTH_FAILED"), (403, "MODEL_AUTH_FAILED"),
                                        (429, "MODEL_RATE_LIMIT"), (408, "MODEL_TIMEOUT"), (503, "MODEL_UNAVAILABLE")])
def test_model_error_classification_does_not_leak_provider_body(status, code):
    async def run():
        client = e.ModelClient(config(max_retries=0), asyncio.Semaphore(1))
        await client.client.aclose()
        client.client = httpx.AsyncClient(transport=httpx.MockTransport(lambda request: httpx.Response(status, text="private secret")))
        with pytest.raises(e.ModelRequestError) as error:
            await client.request("read")
        assert error.value.code == code
        assert "secret" not in str(error.value)
        await client.close()
    asyncio.run(run())


def test_content_filter_is_distinct_and_not_retried_as_invalid_json(tmp_path):
    async def run():
        calls = []
        client = e.ModelClient(config(), asyncio.Semaphore(1))
        await client.client.aclose()
        def response(request):
            calls.append(request)
            return httpx.Response(200, json={"choices": [{"finish_reason": "content_filter", "message": {"content": None}}]})
        client.client = httpx.AsyncClient(transport=httpx.MockTransport(response))
        token = e._DIAGNOSTIC_DIR.set(tmp_path)
        try:
            with pytest.raises(e.ModelContentFilterError) as error:
                await e._transcribe(client, b"image", "read", 2)
            assert error.value.code == "MODEL_CONTENT_FILTER"
            assert len(calls) == 1
            evidence = json.loads(next(tmp_path.glob("response-*.json")).read_text())
            assert evidence["finish_reason"] == "content_filter"
            assert "secret" not in json.dumps(evidence)
        finally:
            e._DIAGNOSTIC_DIR.reset(token)
            await client.close()
    asyncio.run(run())


def test_render_budget_is_bounded(tmp_path):
    source = tmp_path / "source.pdf"
    pdf(source, 1)
    raw, dimensions = e._render(source, 1, 300, 1_000_000)
    assert raw.startswith(b"\x89PNG")
    assert dimensions["image_width"] * dimensions["image_height"] < 1_010_000


def test_local_ocr_confidence_and_provenance_are_not_invented():
    tsv = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n5\t1\t1\t1\t1\t1\t10\t20\t30\t40\t93.5\t人口\n5\t1\t1\t1\t1\t2\t50\t20\t20\t40\t42\t调查\n"
    result = e.parse_ocr_tsv(tsv, 100, 100)
    assert result["text"] == "人口 调查"
    assert result["words"][0]["bbox"] == [.1, .2, .4, .6]
    assert result["low_confidence_words"] == 1
    assert result["lines"][0]["confidence"] == 67.75


def test_content_filter_fallback_keeps_page_without_claiming_table_absence(tmp_path, monkeypatch):
    source = tmp_path / "source.pdf"
    pdf(source, 1)
    async def request(self, prompt, images=()):
        raise e.ModelContentFilterError("Provider blocked this page")
    async def ocr(path, cfg):
        assert path.exists()
        return "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n5\t1\t1\t1\t1\t1\t10\t20\t30\t40\t91\tReport\n"
    monkeypatch.setattr(e.ModelClient, "request", request)
    monkeypatch.setattr(e, "_run_ocr", ocr)
    result = asyncio.run(e.extract_pdf(source, tmp_path / "output", config(fallback_ocr=True)))
    page = result["pages"][0]
    assert page["text"] == "Report"
    assert page["backend"] == "local_ocr"
    assert page["text_present"] is True
    assert page["text_extraction_complete"] is False
    assert page["fallback_reason"]["code"] == "MODEL_CONTENT_FILTER"
    assert page["table_extraction_unavailable"] is True
    assert page["formula_extraction_unavailable"] is True
    assert result["quality"]["semantic_coverage_complete"] is False
    assert result["quality"]["failed_text_pages"] == []
    assert result["quality"]["unverified_text_pages"] == [1]
    assert result["bundle"]["page_context_coverage"] == "incomplete"
    assert result["bundle"]["page_context"][0]["quality"]["status"] == "needs_review"


def test_unit_width_problem_does_not_quarantine_correct_data_cells():
    original = sample()
    original["tables"][0]["units"] = ["persons"]
    page = e.validate_page(original, quarantine_grid=True)
    page["tables"] = e.assess_tables(page, [page, deepcopy(page)], True)
    page.update(page=1, quality={"status": "needs_review"})
    fragment = e.build_bundle({}, [page], {})["fragments"][0]
    assert fragment["units"] == [None, None]
    assert fragment["unit_candidates"] == ["persons"]
    assert fragment["unit_alignment_uncertain"] is True
    assert not any(c.get("uncertain") for row in fragment["rows"] for c in row)


def test_optional_continuation_provider_failure_preserves_complete_pages(tmp_path, monkeypatch):
    source = tmp_path / "source.pdf"
    pdf(source, 2)
    async def request(self, prompt, images=()):
        if not images:
            raise e.ModelContentFilterError("Provider blocked the optional relation request")
        return sample(), {"finish_reason": "stop"}
    monkeypatch.setattr(e.ModelClient, "request", request)
    artifact = asyncio.run(e.extract_pdf(source, tmp_path / "output", config()))
    assert artifact["quality"]["complete_pages"] == 2
    assert artifact["quality"]["status"] == "needs_review"
    assert artifact["bundle"]["continuation_proposals"] == []
    issue = artifact["bundle"]["continuation_issues"][0]
    assert issue["pages"] == [1, 2]
    assert issue["code"] == "MODEL_CONTENT_FILTER"
    assert issue["needs_review"] is True
    assert (tmp_path / "output" / "artifact.json").exists()
