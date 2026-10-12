#!/usr/bin/env python3
"""Run original research jobs under an explicitly verified workspace/account."""
import argparse
from pathlib import Path
import sys
from types import SimpleNamespace

from weknora_api import API, Failure


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("fetch", "compute", "signals", "brief", "snapshot", "daily"))
    parser.add_argument("--email", required=True)
    parser.add_argument("--tenant", type=int)
    parser.add_argument("--date")
    parser.add_argument("--only", nargs="*")
    parser.add_argument("--api", default="http://127.0.0.1:18081")
    parser.add_argument("--accounts", type=Path, default=Path(__file__).resolve().parents[2] / ".private/accounts.json")
    args = parser.parse_args()
    try:
        api = API(args.api, args.tenant)
        data = api.login_email(args.accounts, args.email)
        membership = next((m for m in data.get("memberships", []) if int(m["tenant_id"]) == api.tenant), None)
        if not membership or membership.get("role") not in {"owner", "admin", "contributor"}:
            raise Failure("任务需要当前空间的写入权限")
        from tin.config import settings
        from tin.integration.identity import Identity, current_identity
        from tin.jobs import __main__ as jobs
        # This script operates the integrated database and cannot silently fall back to data/tin.db.
        settings.weknora_api_url = api.url
        user = data["user"]
        identity = Identity(user["id"], user["email"], user.get("username") or user["email"], api.tenant, membership["role"])
        marker = current_identity.set(identity)
        try:
            getattr(jobs, "cmd_" + args.command)(SimpleNamespace(date=args.date, only=args.only))
        finally:
            current_identity.reset(marker)
    except (Failure, OSError, ValueError, KeyError) as exc:
        print(str(exc) if isinstance(exc, Failure) else "研究任务配置或响应格式错误，未输出凭据", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
