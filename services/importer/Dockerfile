# importer: parses broker exports and stages rows (specs/architecture.md). Internal network only.
FROM python:3.12.16-slim AS runtime
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /app
COPY requirements.txt .
RUN pip install --require-hashes -r requirements.txt
# Unprivileged user; /data/files is the files volume (D-12) and is created here so a new volume inherits the owner.
RUN useradd --system --uid 10001 --no-create-home importer \
    && mkdir -p /data/files && chown importer:importer /data/files
COPY importer ./importer
USER importer
EXPOSE 8000
HEALTHCHECK --interval=5s --timeout=3s --retries=20 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/internal/health', timeout=2)"]
CMD ["uvicorn", "importer.main:app", "--host", "0.0.0.0", "--port", "8000", "--no-server-header", "--no-access-log"]

# Test image: the runtime plus pytest and the tests (scripts/importer-test.sh).
FROM runtime AS test
USER root
COPY requirements-dev.txt .
RUN pip install --require-hashes -r requirements-dev.txt
COPY pyproject.toml ./
COPY tests ./tests
USER importer
CMD ["python", "-m", "pytest"]
