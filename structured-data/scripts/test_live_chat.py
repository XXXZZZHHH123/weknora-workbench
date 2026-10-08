#!/usr/bin/env python3
"""Small opt-in WeKnora/LLM trial; never uploads data or starts embeddings."""
import argparse
import datetime as dt
import json
from pathlib import Path
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT.parent))
from run_tests import API, events, login, save


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--ids", default="SD-02,SD-05,SD-06,SD-08")
    parser.add_argument("--cases", default="tests/acceptance_cases.json")
    parser.add_argument("--output", default="results/live-chat")
    args = parser.parse_args()
    ids = args.ids.split(",")
    cases = json.loads((ROOT / args.cases).read_text())["cases"]
    integration = json.loads((ROOT / "results/integration.json").read_text())
    api = API("http://127.0.0.1:18081")
    login(api, json.loads((ROOT.parent / ".private/accounts.json").read_text())[0])
    for case in cases:
        if case["id"] not in ids:
            continue
        target = ROOT / args.output / (case["id"] + ".json")
        if target.exists():
            raise ValueError("Evidence already exists; choose a new output directory")
        sid = api.ok("POST", "/api/v1/sessions", {"title": "结构化试点 " + case["id"], "agent_id": integration["agent_id"]})["data"]["id"]
        payload = {"query": case["question"], "agent_id": integration["agent_id"], "knowledge_base_ids": [], "disable_title": True, "web_search_enabled": False, "local_browser_enabled": False}
        record = {"case_id": case["id"], "question": case["question"], "session_id": sid, "agent_id": integration["agent_id"], "answer": "", "events": [], "status": "not_run", "timestamp": dt.datetime.now(dt.timezone.utc).isoformat()}
        start = time.monotonic()
        try:
            req = api.request("POST", "/api/v1/agent-chat/" + sid, payload)
            with api.opener.open(req, timeout=120) as response:
                for event in events(response):
                    record["events"].append(event)
                    kind = event.get("response_type")
                    if kind == "answer":
                        record["answer"] += event.get("content") or ""
                    if kind == "error":
                        record["status"] = "stream_error"
                        break
                    if kind == "complete":
                        record["stream_answer"] = record["answer"]
                        record["answer"] = (event.get("data") or {}).get("final_content") or record["answer"]
                        record["status"] = "answered_ungraded" if record["answer"].strip() else "empty_answer"
                        break
                    if time.monotonic()-start > 180:
                        raise TimeoutError("small trial exceeded 180 seconds")
        except Exception as exc:
            record["status"] = "execution_error"
            record["error_type"] = type(exc).__name__
        record["elapsed_seconds"] = round(time.monotonic()-start, 3)
        save(target, record)
        print(case["id"], record["status"], record["elapsed_seconds"], flush=True)
        if record["status"] != "answered_ungraded":
            break


if __name__ == "__main__":
    main()
