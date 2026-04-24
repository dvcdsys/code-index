# Embedding Quality Benchmark — gguf/limcheekin/CodeRankEmbed-GGUF/coderankembed.Q5_K_M.gguf vs fp16/nomic-ai/CodeRankEmbed


**k** = 10  |  **queries** = 20  |  **dim ref/cand** = 768/768

## Summary

| Metric | Value | Acceptance |
|---|---:|---:|
| Jaccard@10 (mean) | 0.815 | ≥ 0.70 |
| Recall@10 (mean) | 0.895 | ≥ 0.90 |
| Kendall tau (mean) | 0.786 | ≥ 0.50 |
| Reference embed time | 11.5s | — |
| Candidate embed time | 4.8s | — |
| Speedup (ref/cand) | 2.38× | — |

## Per-query scores

| Query | Jaccard | Recall | Kendall τ |
|---|---:|---:|---:|
| `async queue timeout` | 0.667 | 0.800 | 0.929 |
| `parse tree-sitter chunk` | 0.818 | 0.900 | 0.889 |
| `chroma collection upsert` | 0.818 | 0.900 | 0.722 |
| `cli root command version` | 0.818 | 0.900 | 0.389 |
| `embedding service load model` | 1.000 | 1.000 | 0.867 |
| `project root detection` | 0.818 | 0.900 | 0.889 |
| `file watcher branch switch` | 0.818 | 0.900 | 0.889 |
| `config yaml migration legacy keys` | 0.818 | 0.900 | 0.556 |
| `indexing status estimated finish` | 0.818 | 0.900 | 0.667 |
| `search by meaning code` | 0.818 | 0.900 | 0.833 |
| `api key authentication middleware` | 0.818 | 0.900 | 0.889 |
| `health endpoint status response` | 1.000 | 1.000 | 1.000 |
| `docker compose cuda healthcheck` | 0.818 | 0.900 | 0.889 |
| `gitignore pattern matching` | 1.000 | 1.000 | 0.689 |
| `sqlite projects table schema` | 0.818 | 0.900 | 1.000 |
| `mean pooling embedding` | 0.818 | 0.900 | 0.889 |
| `batch size inference throughput` | 0.667 | 0.800 | 0.857 |
| `incremental reindex sha256` | 0.667 | 0.800 | 0.786 |
| `client version header compatibility` | 0.818 | 0.900 | 0.944 |
| `goroutine concurrent walk` | 0.667 | 0.800 | 0.143 |

Raw top-k lists: `benchmark-data/benchmark-q5_k_m.json`
