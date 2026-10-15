"""Probe the local gRPC service and parser registry using the backend's auth."""

import os
from pathlib import Path
import sys

import grpc
from grpc_health.v1 import health_pb2, health_pb2_grpc

from docreader.proto import docreader_pb2, docreader_pb2_grpc


def check():
    address = os.environ.get("DOCREADER_ADDR", "127.0.0.1:50051")
    token = os.environ.get("GRPC_AUTH_TOKEN", "")
    metadata = (("authorization", "Bearer " + token),) if token else ()
    if os.environ.get("GRPC_TLS_ENABLED") == "true":
        def pem(name):
            path = os.environ.get(name)
            return Path(path).read_bytes() if path else None

        credentials = grpc.ssl_channel_credentials(pem("GRPC_TLS_CA"), pem("GRPC_TLS_KEY"), pem("GRPC_TLS_CERT"))
        server_name = os.environ.get("GRPC_TLS_SERVER_NAME")
        options = (("grpc.ssl_target_name_override", server_name),) if server_name else ()
        channel = grpc.secure_channel(address, credentials, options=options)
    else:
        channel = grpc.insecure_channel(address)
    with channel:
        health = health_pb2_grpc.HealthStub(channel).Check(health_pb2.HealthCheckRequest(), timeout=2)
        if health.status != health_pb2.HealthCheckResponse.SERVING:
            raise RuntimeError("DocReader is not serving")
        engines = docreader_pb2_grpc.DocReaderStub(channel).ListEngines(
            docreader_pb2.ListEnginesRequest(), metadata=metadata, timeout=2)
        if not any(engine.name == "builtin" and engine.available for engine in engines.engines):
            raise RuntimeError("DocReader built-in parsers are unavailable")


if __name__ == "__main__":
    try:
        check()
    except (grpc.RpcError, OSError, RuntimeError):
        print("DocReader gRPC / parser health check failed", file=sys.stderr)
        sys.exit(1)
