from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading
import sys

import pytest

from tin.config import ROOT
sys.path.insert(0,str(ROOT / "scripts"))
from weknora_api import API,Failure
from register_weknora import register


def test_admin_client_does_not_follow_bearer_redirects():
    received=[]
    class Destination(BaseHTTPRequestHandler):
        def do_GET(self):
            received.append(self.headers.get("Authorization"))
            self.send_response(200);self.end_headers();self.wfile.write(b'{"success":true}')
        def log_message(self,*args):pass
    target=ThreadingHTTPServer(("127.0.0.1",0),Destination)
    class Redirect(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(302)
            self.send_header("Location",f"http://127.0.0.1:{target.server_port}/destination")
            self.end_headers()
        def log_message(self,*args):pass
    origin=ThreadingHTTPServer(("127.0.0.1",0),Redirect)
    threads=[threading.Thread(target=s.serve_forever,daemon=True) for s in (target,origin)]
    for thread in threads:thread.start()
    try:
        api=API(f"http://127.0.0.1:{origin.server_port}");api.token="fake-test-bearer"
        with pytest.raises(Failure,match="HTTP 302"):
            api.call("GET","/api/v1/auth/me")
        assert received == []
    finally:
        for server in (target,origin):server.shutdown();server.server_close()
        for thread in threads:thread.join(timeout=2)


class FakeAPI:
    tenant=7
    def __init__(self,agents=()):self.agents=list(agents);self.writes=[]
    def call(self,method,path,body=None):
        if method != "GET":
            self.writes.append((method,path,body));return {"data":{"id":"registered"}}
        return {"data":([{ "id":"chosen","type":"KnowledgeQA","status":"active","name":"Model"}]
                         if path.endswith("models") else self.agents)}


def test_registration_preview_and_unowned_collision_are_readonly():
    api=FakeAPI();result=register(api)
    assert result["mode"] == "preview" and api.writes == []
    api=FakeAPI([{"id":"someone","name":"商品研究助手","description":"user's unrelated agent"}])
    with pytest.raises(Failure,match="拒绝覆盖"):register(api,apply=True)
    assert api.writes == []


def test_registration_has_readonly_opt_in_tool_and_existing_model():
    api=FakeAPI();result=register(api,apply=True)
    assert result["agent_id"] == "registered"
    payload=api.writes[0][2]
    assert payload["config"]["allowed_tools"] == ["commodity_research"]
    assert payload["config"]["model_id"] == "chosen"
    assert not payload["config"]["web_search_enabled"]
