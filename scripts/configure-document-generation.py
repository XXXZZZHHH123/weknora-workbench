#!/usr/bin/env python3
"""Install the document skill through official workspace APIs (admin only)."""
import argparse
import io
import json
import os
from pathlib import Path
import sys
import time
from urllib.request import Request
import uuid
import zipfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "commodity-research/scripts"))
from weknora_api import API, Failure, rows, unpack


def bundle():
    content = io.BytesIO()
    folder = ROOT / "skills/document-export"
    with zipfile.ZipFile(content, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(folder.rglob("*")):
            if path.is_file() and "__pycache__" not in path.parts and "tests" not in path.parts:
                archive.write(path, "document-export/" + str(path.relative_to(folder)))
    return content.getvalue()


def upload(api, config_id):
    boundary = "weknora-" + uuid.uuid4().hex
    data = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="document-export.zip"\r\n'
            'Content-Type: application/zip\r\n\r\n').encode() + bundle() + f"\r\n--{boundary}--\r\n".encode()
    request = Request(api.url + f"/api/v1/sandbox-configs/{config_id}/skills", data=data, method="POST", headers={
        "Content-Type": f"multipart/form-data; boundary={boundary}",
        "Authorization": "Bearer " + api.token, "X-Tenant-ID": str(api.tenant),
    })
    with api.opener.open(request, timeout=30) as response:
        return json.load(response)["data"]["skill_id"]


def configure(api, image, agents, config_name, wait_seconds):
    configs = rows(api.call("GET", "/api/v1/sandbox-configs"))
    matches = [row for row in configs if row["name"] == config_name]
    if len(matches) > 1:
        raise Failure("沙箱名称不唯一，请先在设置中核对")
    targets = []
    existing_id = matches[0]["id"] if matches else None
    for agent_id in agents:
        agent = unpack(api.call("GET", "/api/v1/agents/" + agent_id))
        if int(agent.get("tenant_id", 0)) != api.tenant:
            raise Failure("只能配置当前空间的智能体")
        if agent["config"].get("sandbox_config_id") not in {None, "", existing_id}:
            raise Failure(f"智能体 {agent_id} 已绑定其他沙箱，保留原配置")
        targets.append(agent)
    if matches:
        config = matches[0]
        if config.get("sandbox_type") != "docker" or config.get("config", {}).get("docker", {}).get("image") != image:
            raise Failure("已有同名沙箱的后端或镜像不同，保留原配置；请显式选择新名称")
    else:
        config = unpack(api.call("POST", "/api/v1/sandbox-configs", {
            "name": config_name, "description": "普通对话的文档生成环境，沿用官方 Skills 和会话产物机制",
            "config": {"sandbox_type": "docker", "default_timeout_sec": 120,
                       "docker": {"image": image, "cpu_limit": 2, "memory_limit_mb": 2048, "pids_limit": 512, "network_mode": "none", "idle_ttl_seconds": 1800}},
        }))
    config_id = config["id"]
    # The image includes every dependency, so execution and installation need no egress.
    skill_id = upload(api, config_id)
    deadline = time.monotonic() + wait_seconds
    previous = None
    while True:
        skill = unpack(api.call("GET", f"/api/v1/sandbox-configs/{config_id}/skills/{skill_id}"))
        status = skill.get("status")
        if status != previous:
            print("文档技能安装状态:", status, flush=True)
            previous = status
        if status == "ready":
            break
        if status == "failed":
            raise Failure("技能安装失败，未修改智能体；请在技能安装记录查看原因")
        if time.monotonic() >= deadline:
            raise Failure("安装仍在运行，未修改智能体；请查看官方安装进度，勿重复提交")
        time.sleep(5)
    available = rows(api.call("GET", f"/api/v1/skills?sandbox_config_id={config_id}"))
    if not any(row.get("name") == "document-export" for row in available):
        raise Failure("技能未通过实际可用性检查，未修改智能体")
    backup_dir = ROOT / ".private/document-generation"
    backup_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    for before in targets:
        agent_id = before["id"]
        agent = unpack(api.call("GET", "/api/v1/agents/" + agent_id))
        if agent["config"] != before["config"]:
            raise Failure("安装期间智能体配置已被修改，保留新配置，请核对后重试")
        saved_config = agent["config"]
        # Do not silently replace another approved environment or a disabled
        # workspace policy. Only the explicitly selected agents are modified.
        if saved_config.get("sandbox_config_id") not in {None, "", config_id}:
            raise Failure(f"智能体 {agent_id} 已绑定其他沙箱，保留原配置")
        backup = backup_dir / f"{agent_id}-{uuid.uuid4().hex[:8]}.json"
        with open(backup, "x", opener=lambda path, flags: os.open(path, flags, 0o600)) as output:
            json.dump(agent, output, ensure_ascii=False)
        updated = dict(saved_config)
        updated["sandbox_config_id"] = config_id
        mode = updated.get("skills_selection_mode")
        if mode != "all":
            updated["skills_selection_mode"] = "selected"
            updated["selected_skills"] = list(dict.fromkeys([*(updated.get("selected_skills") or []), "document-export"]))
        api.call("PUT", "/api/v1/agents/" + agent_id, {"name": agent["name"], "description": agent.get("description", ""), "config": updated})
        stored = unpack(api.call("GET", "/api/v1/agents/" + agent_id))["config"]
        if stored.get("sandbox_config_id") != config_id or stored.get("skills_selection_mode") == "none":
            raise Failure("智能体保存后核验失败，请使用私有备份恢复")
        print("已接通智能体:", agent_id, flush=True)
    return {"sandbox_config_id": config_id, "skill_id": skill_id, "agents": agents}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    auth = parser.add_mutually_exclusive_group(required=True)
    auth.add_argument("--token-file", type=Path)
    auth.add_argument("--accounts-file", type=Path)
    parser.add_argument("--email")
    parser.add_argument("--image", required=True)
    parser.add_argument("--name", default="文档生成")
    parser.add_argument("--agents", nargs="+", default=["builtin-quick-answer", "builtin-smart-reasoning"])
    parser.add_argument("--wait-seconds", type=int, default=900)
    args = parser.parse_args()
    api = API(args.url)
    if args.token_file:
        api.token = args.token_file.read_text().strip()
        me = unpack(api.call("GET", "/api/v1/auth/me"))
        api.tenant = int(me["tenant"]["id"])
    else:
        if not args.email:
            parser.error("--accounts-file 需要 --email")
        me = api.login_email(args.accounts_file, args.email)
    # Server APIs enforce the authenticated workspace's Admin/Owner role.
    print(json.dumps(configure(api, args.image, args.agents, args.name, args.wait_seconds), ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        # HTTPError includes only the endpoint/status here, never auth headers.
        print(f"配置未完成: {exc}", file=sys.stderr)
        raise SystemExit(1)
