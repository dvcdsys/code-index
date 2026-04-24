# Embedding Quality Benchmark — gguf/limcheekin/CodeRankEmbed-GGUF/coderankembed.Q4_K_M.gguf vs fp16/nomic-ai/CodeRankEmbed


**k** = 10  |  **queries** = 20  |  **dim ref/cand** = 768/768

## Summary

| Metric | Value | Acceptance |
|---|---:|---:|
| Jaccard@10 (mean) | 0.787 | ≥ 0.70 |
| Recall@10 (mean) | 0.875 | ≥ 0.90 |
| Kendall tau (mean) | 0.760 | ≥ 0.50 |
| Reference embed time | 11.5s | — |
| Candidate embed time | 4.6s | — |
| Speedup (ref/cand) | 2.51× | — |

## Per-query scores

| Query | Jaccard | Recall | Kendall τ |
|---|---:|---:|---:|
| `async queue timeout` | 0.667 | 0.800 | 0.786 |
| `parse tree-sitter chunk` | 1.000 | 1.000 | 0.867 |
| `chroma collection upsert` | 0.818 | 0.900 | 0.778 |
| `cli root command version` | 0.818 | 0.900 | 0.611 |
| `embedding service load model` | 1.000 | 1.000 | 0.600 |
| `project root detection` | 0.818 | 0.900 | 0.833 |
| `file watcher branch switch` | 0.538 | 0.700 | 0.810 |
| `config yaml migration legacy keys` | 0.818 | 0.900 | 0.667 |
| `indexing status estimated finish` | 1.000 | 1.000 | 0.822 |
| `search by meaning code` | 0.818 | 0.900 | 0.778 |
| `api key authentication middleware` | 0.818 | 0.900 | 0.889 |
| `health endpoint status response` | 0.818 | 0.900 | 0.833 |
| `docker compose cuda healthcheck` | 0.818 | 0.900 | 0.667 |
| `gitignore pattern matching` | 0.818 | 0.900 | 0.722 |
| `sqlite projects table schema` | 0.818 | 0.900 | 0.944 |
| `mean pooling embedding` | 0.818 | 0.900 | 0.944 |
| `batch size inference throughput` | 0.667 | 0.800 | 0.714 |
| `incremental reindex sha256` | 0.667 | 0.800 | 0.857 |
| `client version header compatibility` | 0.667 | 0.800 | 0.929 |
| `goroutine concurrent walk` | 0.538 | 0.700 | 0.143 |

Raw top-k lists: `benchmark-data/benchmark-q4_k_m.json`
