# Stage 1: builder — install deps + download model
FROM python:3.14-slim AS builder
WORKDIR /build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential gcc && rm -rf /var/lib/apt/lists/*
COPY api/requirements.txt .
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt
# Pre-download embedding model at build time (~274MB)
RUN PYTHONPATH=/install/lib/python3.12/site-packages python -c \
    "from sentence_transformers import SentenceTransformer; SentenceTransformer('nomic-ai/CodeRankEmbed', trust_remote_code=True)"

# Stage 2: runtime
FROM python:3.14-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
COPY --from=builder /install /usr/local
COPY --from=builder /root/.cache/huggingface /root/.cache/huggingface
WORKDIR /app
COPY api/app/ ./app/
RUN mkdir -p /data/chroma /data/sqlite
EXPOSE 21847
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD curl -f http://localhost:21847/health || exit 1
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "21847"]
