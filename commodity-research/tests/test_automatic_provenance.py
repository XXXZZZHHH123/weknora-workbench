from datetime import date, datetime, timedelta
from types import SimpleNamespace

from sqlalchemy import event, func, select

from tin.models import Observation, ObservationSource
from tin.ingest import fred
from tin.ingest.runner import store
from tin.provenance import automatic


def test_csv_location_index_preserves_first_match_and_original_cells(monkeypatch):
    original = automatic.csv.DictReader
    parses = []

    def reader(*args, **kwargs):
        parses.append(1)
        return original(*args, **kwargs)

    monkeypatch.setattr(automatic.csv, "DictReader", reader)
    data = b"DATE,RATE\n2026-09-18,4.2\ninvalid,.\n2026-09-19,4.3\n2026-09-18,9\n"
    prepared = {}
    first = SimpleNamespace(source="FRED", as_of=datetime(2026, 9, 18), series_id="FRED.DGS2")
    second = SimpleNamespace(source="FRED", as_of=datetime(2026, 9, 19), series_id="FRED.DGS2")
    a = automatic.locate_document(data, first, prepared=prepared)
    b = automatic.locate_document(data, second, prepared=prepared)
    assert a["value_cell"] == "B2" and a["raw_value"] == "4.2" and a["date_cell"] == "A2"
    assert b["value_cell"] == "B4" and b["raw_value"] == "4.3" and b["header_cells"] == ["B1"]
    assert len(parses) == 1
    # A new response receives a new run-local cache even when dates repeat.
    assert automatic.locate_document(b"DATE,RATE\n2026-09-18,5\n", first)["raw_value"] == "5"
    assert len(parses) == 2


def test_automatic_store_prefetches_revisions_and_keeps_idempotent_evidence(session):
    start = date(2026, 9, 1)
    days = [start + timedelta(days=i) for i in range(30)]
    spec = fred.SERIES_BY_ID["FRED.DGS2"]
    url = "https://fred.example/series.csv"

    def imported_blob(values, extra=""):
        data = ("DATE,DGS2\n" + "\n".join(f"{d},{value}" for d, value in zip(days, values)) + extra).encode()
        batch = fred.parse_series(data.decode(), spec, through=days[-1], source_url=url)
        return batch, [{"data": data, "filename": "series.csv", "source_url": url}]

    values = [float(i + 1) for i in range(30)]
    batch, documents = imported_blob(values)
    assert store(session, batch, documents=documents, batch_label="fred-test") == 30
    session.commit()
    queries = []

    def query(_, __, statement, *args):
        if statement.lstrip().upper().startswith("SELECT"):
            queries.append(statement)

    engine = session.get_bind()
    event.listen(engine, "before_cursor_execute", query)
    try:
        assert store(session, batch, documents=documents, batch_label="fred-test") == 0
    finally:
        event.remove(engine, "before_cursor_execute", query)
    assert sum("FROM observations" in statement for statement in queries) == 1
    assert sum("FROM indicators" in statement for statement in queries) <= 2
    assert sum("FROM observation_sources" in statement for statement in queries) == 1
    assert session.scalar(select(func.count()).select_from(ObservationSource)) == 30
    # A genuinely new immutable response supplies evidence for unchanged values
    # and appends a revision for a changed value through the same record gate.
    changed, documents = imported_blob([values[0] + 1, *values[1:]], "\n")
    assert store(session, changed, documents=documents, batch_label="fred-test") == 1
    assert session.scalar(select(func.count()).select_from(Observation)) == 31
    assert session.scalar(select(func.count()).select_from(ObservationSource)) == 60
