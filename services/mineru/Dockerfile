FROM python:3.12.15-slim-bookworm

LABEL org.opencontainers.image.title="WeKnora MinerU Standard CPU worker" \
      org.opencontainers.image.version="4.0.12"

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    MINERU_HOME=/var/lib/mineru/data/home/.mineru \
    MINERU_MODEL_BASE_DIR=/var/lib/mineru/models \
    MINERU_MODEL_SMALL_BACKEND=onnx \
    MINERU_MODEL_SOURCE=local \
    MINERU_LOG_LEVEL=warning \
    MINERU_API_ENABLE_FASTAPI_DOCS=false \
    OMP_NUM_THREADS=2 \
    OPENBLAS_NUM_THREADS=2 \
    MKL_NUM_THREADS=2 \
    HOME=/var/lib/mineru/data/home

RUN apt-get update \
    && apt-get install -y --no-install-recommends libglib2.0-0 libgl1 libgomp1 libvulkan1 \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
       'mineru==4.0.12' 'onnxruntime==1.31.0' 'mineru-llama-cpp==0.1.4' \
       'docvortex==0.5.18' 'mineru-vl-utils==2.0.7' 'uvicorn==0.54.0' \
    && python -c "from importlib.metadata import version; import importlib.util; assert version('mineru') == '4.0.12'; assert importlib.util.find_spec('torch') is None; assert importlib.util.find_spec('onnxruntime') is not None" \
    && groupadd --gid 10001 mineru \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin mineru \
    && mkdir -p /var/lib/mineru/data/home /var/lib/mineru/models /opt/mineru \
    && chown -R 10001:10001 /var/lib/mineru /opt/mineru

COPY --chown=10001:10001 entrypoint.py /opt/mineru/entrypoint.py
USER 10001:10001
WORKDIR /opt/mineru
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=10s --start-period=180s --retries=3 \
    CMD ["python", "/opt/mineru/entrypoint.py", "--healthcheck"]
ENTRYPOINT ["python", "/opt/mineru/entrypoint.py"]
