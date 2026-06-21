# Literature-Guided Redesign

Dokumen ini merangkum arah redesign `agent-explorer` berdasarkan paper dan referensi agent best practice, lalu memetakan gap implementasi saat ini.

## Ringkas

Masalah utama sekarang bukan lagi "planner kurang pintar", tapi:

1. planner sudah bisa mengeluarkan intent/slot,
2. namun retrieval per-slot masih memakai search global repo,
3. sehingga slot `consumer`, `detector`, `retry`, `projection`, `reconcile` masih saling mencemari.

Akibat:
- auth query bisa terseret ke `claims.py`
- backfill query bisa terseret ke `pkg/mq/retry.go`
- parity query bisa terseret ke `source_mirror/manifest_reconciler.go`

## Referensi kunci

### 1. RANGER: Repository-level Agent for Graph-Enhanced Retrieval
https://arxiv.org/html/2509.25257v1

Implikasi:
- jangan pakai satu strategi retrieval untuk semua query
- query NL butuh graph exploration terarah
- entity query dan behavior query harus di-route berbeda

### 2. Lexically Anchored Repository Graph Exploration and Retrieval
https://arxiv.org/html/2605.16352

Implikasi:
- lexical anchor tetap penting
- tapi anchor hanya entrypoint
- retrieval final harus memanfaatkan relasi graph, bukan token match saja

### 3. LLM Agents Improve Semantic Code Search
https://arxiv.org/html/2408.11058v1

Implikasi:
- decomposition/agent loop memang membantu
- tapi hasil naik hanya bila retrieval loop dan reranking tepat

### 4. GraphCodeAgent
https://arxiv.org/html/2504.10046v2

Implikasi:
- natural language requirements sering memerlukan implicit code element
- multi-hop retrieval perlu dukungan graph

### 5. CORE-Bench
https://arxiv.org/html/2606.11864v1

Implikasi:
- benchmark retrieval harus requirement-driven
- jangan puas dengan file hit umum; ukur localization yang benar

### 6. Anthropic: Effective context engineering for AI agents
https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents

Implikasi:
- context harus kecil, relevan, just-in-time
- hit yang tidak membantu slot penting harus dibuang lebih awal

## Arah redesign

### Fase 1: planner tetap ringkas

Planner hanya bertugas:
- klasifikasi query
- menentukan `primary_tool`
- menentukan `slots`
- menentukan `need_call_graph`

Planner tidak boleh terlalu bebas menulis narasi retrieval.

### Fase 2: per-slot retrieval

Setiap slot harus punya:
- role canonical
- allowed tool families
- hints
- preferred anchor scope

Contoh:
- `validator` -> `graph_text`, `graph`
- `injector` -> `graph`, lalu `graph_text`
- `consumer` -> `graph`, `trace`
- `detector` -> `graph_text`, `rg`, lalu `graph`
- `retry` -> `graph`, `rg`

### Fase 3: scoped graph search

Ini gap terbesar saat ini.

Sebelum `search_graph`, sistem perlu:
- hitung cluster anchor
- pilih 1-3 direktori paling relevan
- jalankan retrieval hanya untuk cluster itu bila memungkinkan

Tanpa scope narrowing, graph search terlalu mudah terseret repo-global false positive.

### Fase 4: slot grader

Jangan pakai satu `rankHit` global saja.

Harus ada grader per slot:
- `validator`: cari validate/verify/auth/jwt/token/bearer
- `injector`: cari context storage / claims set
- `consumer`: cari handler/controller reading claims/context
- `detector`: cari detect/watch/gap/stall
- `retry`: cari retry/requeue/backoff
- `projection`: cari projection/current/publish
- `reconcile`: cari reconcile/rebuild/repair

### Fase 5: final answer by slot

Output akhir harus:
- minimal 1 best hit per required slot
- urut sesuai slot importance
- bukan sekadar top-N global rank

## Common pitfalls

1. `global graph bias`
Graph search bagus, tapi tanpa scope narrowing dia terlalu global.

2. `semantic contamination`
Semantic hit bagus untuk recall, jelek sebagai final evidence tanpa filter.

3. `slot collapse`
Semua subproblem disatukan ke ranking global. Hasil: satu slot mendominasi, slot lain hilang.

4. `planner verbosity`
Subquery naratif seperti "find where X is implemented" membebani retrieval. Perlu query compaction.

5. `benchmark optimism`
Kalau eval hanya cek "ada hit yang benar di mana saja", sistem terlihat bagus padahal top hit salah.

## Status implementasi saat ini

Sudah ada:
- planner outcome-first
- canonical slots
- per-slot query generation
- role-aware rerank dasar
- repo-specific concept overlay via profile

Belum ada:
- per-slot path scoping / anchor scoping keras
- slot-specific negative filters yang cukup kuat
- trace-as-evidence untuk `consumer` dan `caller/callee`
- fast eval mode yang murah tapi representatif

## Prioritas berikutnya

1. Tambah scoped graph retrieval berdasarkan anchor cluster.
2. Tambah `trace` sebagai fallback khusus slot `consumer` / `caller`.
3. Pecah `rankHit` menjadi `slotHitScore(role, hit)`.
4. Eval ulang dengan grader top-hit + slot coverage + latency.
