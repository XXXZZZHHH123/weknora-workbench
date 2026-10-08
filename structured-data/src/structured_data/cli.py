import argparse
import json
from pathlib import Path

from .store import DataStore, json_default


def main():
    parser = argparse.ArgumentParser(description="General structured Excel workbench")
    parser.add_argument("--data-dir", default="data")
    sub = parser.add_subparsers(dest="command", required=True)
    imp = sub.add_parser("import")
    imp.add_argument("file", type=Path)
    scan = sub.add_parser("scan-import")
    scan.add_argument("file", type=Path)
    sub.add_parser("list")
    server = sub.add_parser("serve")
    server.add_argument("--host", default="127.0.0.1")
    server.add_argument("--port", type=int, default=18082)
    args = parser.parse_args()
    if args.command == "serve":
        import uvicorn
        from .server import create_app

        uvicorn.run(create_app(args.data_dir), host=args.host, port=args.port)
        return
    if args.command in {"scan-import", "import"}:
        from .imports import ImportManager
        print(json.dumps(ImportManager(args.data_dir).ingest(args.file), ensure_ascii=False, indent=2))
        return
    store = DataStore(args.data_dir)
    result = store.list_datasets()
    print(json.dumps(result, ensure_ascii=False, indent=2, default=json_default))


if __name__ == "__main__":
    main()
