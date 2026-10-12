FROM python:3.12-slim-bookworm

ARG RELEASE_SHA=unknown

LABEL org.opencontainers.image.title="Commodity Research Platform" \
      org.opencontainers.image.revision="$RELEASE_SHA"

ENV TIN_DATA_DIR=/data \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    TZ=Asia/Shanghai

RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates tzdata gosu \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt requirements-production.txt pyproject.toml ./
RUN python -m pip install --no-cache-dir -r requirements-production.txt

COPY alembic.ini ./
COPY alembic ./alembic
COPY seeds ./seeds
COPY scripts ./scripts
COPY src ./src
RUN python -m pip install --no-cache-dir --no-deps -e . \
    && groupadd --gid 10001 commodity \
    && useradd --uid 10001 --gid commodity --no-create-home --shell /usr/sbin/nologin commodity \
    && chmod -R a+rX /app \
    && mkdir -p /data /run/commodity \
    && chown commodity:commodity /data /run/commodity

COPY --chmod=0644 deploy/container/backup.py /app/deploy/container/backup.py
COPY --chmod=0755 deploy/container/entrypoint.sh /usr/local/bin/commodity-entrypoint

# Only entrypoint initialization runs as root; uvicorn always drops to UID 10001.
# It copies a host-only 0600 secret without changing the source file permissions.

ENV TIN_EXPORTS_DIR=/data/exports TIN_STAGING_DIR=/data/import_staging

EXPOSE 8765

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
  CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8765/healthz', timeout=2)"]

ENTRYPOINT ["commodity-entrypoint"]
CMD ["python", "-m", "uvicorn", "tin.web.app:app", "--host", "0.0.0.0", "--port", "8765", "--proxy-headers"]
