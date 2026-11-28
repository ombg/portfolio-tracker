# market-data: prices and FX from Yahoo and the Bank of Canada (specs/market-data.md, D-39). The only service with
# internet access; the scheduler and internal API follow in #27.
FROM python:3.12.16-slim AS runtime
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /app
COPY requirements.txt .
RUN pip install --require-hashes -r requirements.txt
RUN useradd --system --uid 10002 --no-create-home market
COPY marketdata ./marketdata
USER market

# Test image: the runtime plus pytest and the tests (scripts/market-data-test.sh).
FROM runtime AS test
USER root
COPY requirements-dev.txt .
RUN pip install --require-hashes -r requirements-dev.txt
COPY pyproject.toml ./
COPY tests ./tests
USER market
CMD ["python", "-m", "pytest"]
