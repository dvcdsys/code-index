# Stage 1: builder — compile deps on Ubuntu 24.04
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    python3 python3-dev python3-venv python3-pip \
    build-essential gcc curl cmake \
    && rm -rf /var/lib/apt/lists/* \
    && update-alternatives --install /usr/bin/python python /usr/bin/python3 1

# Clean pip/setuptools/wheel and reinstall patched versions
RUN rm -f /usr/lib/python3.12/EXTERNALLY-MANAGED && \
    apt-get purge -y python3-setuptools python3-wheel python3-pip 2>/dev/null; \
    curl -sS https://bootstrap.pypa.io/get-pip.py | python && \
    pip install --no-cache-dir "setuptools>=78.1.1" "wheel>=0.46.2"

# Install all deps (llama-cpp-python will be compiled for CPU)
WORKDIR /build
COPY api/requirements.txt .
RUN CMAKE_ARGS="-DLLAMA_BLAS=ON -DLLAMA_BLAS_VENDOR=OpenBLAS" \
    pip install --no-cache-dir --prefix=/install -r requirements.txt && \
    pip install --no-cache-dir --prefix=/install --force-reinstall --no-deps packaging

# Pre-download embedding model at build time
ARG EMBEDDING_MODEL="awhiteside/CodeRankEmbed-Q8_0-GGUF"
RUN PYTHONPATH=/install/local/lib/python3.12/dist-packages python -c \
    "from huggingface_hub import hf_hub_download, list_repo_files; \
    files = list_repo_files('${EMBEDDING_MODEL}'); \
    gguf_file = next((f for f in files if f.endswith('.gguf')), None); \
    hf_hub_download(repo_id='${EMBEDDING_MODEL}', filename=gguf_file)"

# Stage 2: runtime — lightweight image without compilers
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    python3 curl libopenblas-dev \
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
