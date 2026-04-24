# Embedding Quality Benchmark — gguf/limcheekin/CodeRankEmbed-GGUF/coderankembed.F16.gguf vs fp16/nomic-ai/CodeRankEmbed


**k** = 10  |  **queries** = 20  |  **dim ref/cand** = 768/768

## Summary

| Metric | Value | Acceptance |
|---|---:|---:|
| Jaccard@10 (mean) | 0.894 | ≥ 0.70 |
| Recall@10 (mean) | 0.940 | ≥ 0.90 |
| Kendall tau (mean) | 0.879 | ≥ 0.50 |
| Reference embed time | 11.5s | — |
| Candidate embed time | 4.2s | — |
| Speedup (ref/cand) | 2.72× | — |

## Per-query scores

| Query | Jaccard | Recall | Kendall τ |
|---|---:|---:|---:|
| `async queue timeout` | 0.818 | 0.900 | 0.889 |
| `parse tree-sitter chunk` | 1.000 | 1.000 | 0.911 |
| `chroma collection upsert` | 0.818 | 0.900 | 1.000 |
| `cli root command version` | 1.000 | 1.000 | 0.556 |
| `embedding service load model` | 1.000 | 1.000 | 0.956 |
| `project root detection` | 0.818 | 0.900 | 0.889 |
| `file watcher branch switch` | 0.667 | 0.800 | 0.714 |
| `config yaml migration legacy keys` | 1.000 | 1.000 | 0.689 |
| `indexing status estimated finish` | 1.000 | 1.000 | 1.000 |
| `search by meaning code` | 0.818 | 0.900 | 1.000 |
| `api key authentication middleware` | 0.818 | 0.900 | 0.944 |
| `health endpoint status response` | 1.000 | 1.000 | 1.000 |
| `docker compose cuda healthcheck` | 0.818 | 0.900 | 0.944 |
| `gitignore pattern matching` | 1.000 | 1.000 | 0.733 |
| `sqlite projects table schema` | 1.000 | 1.000 | 1.000 |
| `mean pooling embedding` | 1.000 | 1.000 | 0.911 |
| `batch size inference throughput` | 0.818 | 0.900 | 0.778 |
| `incremental reindex sha256` | 1.000 | 1.000 | 0.867 |
| `client version header compatibility` | 0.818 | 0.900 | 0.944 |
| `goroutine concurrent walk` | 0.667 | 0.800 | 0.857 |

Raw top-k lists: `benchmark-data/benchmark-f16.json`
