# Stage 1: builder — compile deps on Ubuntu 24.04
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    python3 python3-dev python3-venv python3-pip \
    build-essential gcc curl \
    && rm -rf /var/lib/apt/lists/* \
    && update-alternatives --install /usr/bin/python python /usr/bin/python3 1

# Clean pip/setuptools/wheel and reinstall patched versions
RUN rm -f /usr/lib/python3.12/EXTERNALLY-MANAGED && \
    apt-get purge -y python3-setuptools python3-wheel python3-pip 2>/dev/null; \
    curl -sS https://bootstrap.pypa.io/get-pip.py | python && \
    pip install --no-cache-dir "setuptools>=78.1.1" "wheel>=0.46.2"

# Install all deps with CPU-only PyTorch (no CUDA libraries)
WORKDIR /build
COPY api/requirements.txt .
RUN pip install --no-cache-dir --prefix=/install \
    --extra-index-url https://download.pytorch.org/whl/cpu \
    -r requirements.txt && \
    pip install --no-cache-dir --prefix=/install --force-reinstall --no-deps packaging

# Pre-download embedding model at build time (~274MB)
RUN PYTHONPATH=/install/local/lib/python3.12/dist-packages python -c \
    "from sentence_transformers import SentenceTransformer; SentenceTransformer('nomic-ai/CodeRankEmbed', trust_remote_code=True)"

# Stage 2: runtime — lightweight image without compilers
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    python3 curl \
    && rm -rf /var/lib/apt/lists/* \
    && update-alternatives --install /usr/bin/python python /usr/bin/python3 1

# Copy installed Python packages and model from builder
COPY --from=builder /install/local /usr/local
COPY --from=builder /root/.cache/huggingface /root/.cache/huggingface

WORKDIR /app
COPY api/app/ ./app/
RUN mkdir -p /data/chroma /data/sqlite
EXPOSE 21847
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD curl -f http://localhost:21847/health || exit 1
CMD ["python", "-m", "uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "21847"]
