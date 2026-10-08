FROM python:3.12-slim
WORKDIR /app
COPY pyproject.toml /app/
COPY src /app/src
RUN pip install --no-cache-dir .
ENV STRUCTURED_DATA_DIR=/data
EXPOSE 18082
CMD ["structured-data", "--data-dir", "/data", "serve", "--host", "0.0.0.0", "--port", "18082"]
