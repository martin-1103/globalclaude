# Retrieval Best Practices

Tujuan dokumen ini: jadi dasar desain `agent-explorer` untuk query explore kompleks, cepat, dan akurat lintas project.

## Prinsip

1. Query routing dulu, retrieval belakangan.
   Jangan lempar semua query ke semantic. Bedakan:
   - definition / caller / callee / symbol -> graph
   - literal / error / config / exact string -> rg
   - structure / syntax shape -> ast-grep
   - behavior / concept / multi-hop domain flow -> graph_text lalu graph, semantic jadi pelengkap

2. Multi-part query harus dipecah.
   Satu query sering punya 2-3 intent:
   - "where is X"
   - "how Y handled"
   - "who consumes Z"
   Tiap intent butuh bukti sendiri. Stop hanya kalau semua intent utama sudah punya evidence.

3. Hybrid retrieval lebih aman dari single-lane retrieval.
   Flat semantic retrieval rawan:
   - ketarik test file
   - ketarik kata umum seperti `failure`, `context`, `flow`
   - miss symbol atau path produksi yang tidak punya surface text bagus

4. Query compaction penting.
   Tool search sering lebih bagus pada phrase pendek berbasis konsep daripada kalimat penuh.
   Contoh:
   - buruk: `how parity failures are handled and where current projection or reconcile path is involved`
   - lebih baik: `parity failures`, `projection reconcile`, `parity projection reconcile`

5. Production-first rerank.
   Final ranking harus keras membuang:
   - `*_test.go`
   - `/tests/`
   - e2e harness
   - scripts deprecated
   - docs
   kecuali query memang eksplisit minta area itu.

6. Stop rule harus evidence-based.
   Jangan stop hanya karena "sudah ada hit". Stop kalau:
   - coverage subquery cukup
   - confidence minimal medium/high
   - ada lane diversity atau grounding structural

## Common Pitfalls

1. Lexical hijack.
   Kata seperti `failure`, `trace`, `flow`, `context`, `detect` sering menarik hasil salah.

2. Semantic-only overreach.
   Semantic bagus buat recall, jelek kalau langsung jadi jawaban final tanpa grounding graph/path.

3. Test pollution.
   Banyak repo punya test surface lebih "deskriptif" dari production code, jadi rank mentah sering salah.

4. Over-fanout.
   Terlalu banyak variant query memang bisa naikkan recall, tapi latency naik tajam. Variant harus dibatasi.

5. Symbol-fragment bias.
   Search graph bisa mengangkat konstanta/helper kecil, bukan method inti. Perlu rerank berbasis path, symbol shape, dan context.

## Tambahan best practice dari literatur

7. Hybrid + rerank, jangan single-stage.
   Benchmark 2026 untuk dokumen campuran text+table menunjukkan dua tahap `hybrid retrieval + neural reranking` unggul jelas atas single-stage, dan BM25 kadang tetap mengalahkan dense retrieval di domain presisi tinggi.

8. Adaptive retrieval harus hemat dan selektif.
   Self-RAG dan paper adaptive retrieval 2025 sama-sama menekankan retrieval tidak boleh selalu dipanggil fix-pattern. Trigger retrieval saat perlu, dan boleh abstain/stop saat bukti lemah.

9. Eval retrieval harus pakai ranking metric, bukan pass/fail saja.
   Minimal:
   - `MRR`
   - `Recall@k`
   - latency p50/p95
   - weak-evidence rate / abstain rate

10. Long context bukan alasan buang rerank.
   "Lost in the Middle" menunjukkan evidence yang benar masih bisa tenggelam kalau ordering/context packing buruk. Karena itu retrieval pack harus pendek, diurutkan, dan tidak noisy.

11. Coverage set lebih penting dari top-1 tunggal untuk query multi-hop.
   Paper multi-hop terbaru menunjukkan ranking per-hit saja sering kurang; retrieval set harus menutup kebutuhan informasi utama, bukan cuma punya satu hit yang kelihatan bagus.

12. Query decomposition dipakai selektif, bukan default brutal.
   Literatur best-practice menunjukkan decomposition kadang bantu multi-hop, tapi bisa rugi di latency dan noise. Pakai hanya saat query benar-benar multi-part atau entity-nya belum lengkap.

## Referensi primer

1. Wang et al., "Searching for Best Practices in Retrieval-Augmented Generation", arXiv:2407.01219.
   https://arxiv.org/abs/2407.01219

2. Asai et al., "Self-RAG: Learning to Retrieve, Generate, and Critique through Self-Reflection", ICLR 2024.
   https://arxiv.org/abs/2310.11511

3. Akarsu et al., "From BM25 to Corrective RAG: Benchmarking Retrieval Strategies for Text-and-Table Documents", arXiv:2604.01733.
   https://arxiv.org/abs/2604.01733

4. Klesel et al., "Adaptive Retrieval without Self-Knowledge? Bringing Uncertainty to Adaptive RAG", ACL 2025.
   https://aclanthology.org/2025.acl-long.319/

5. Liu et al., "Lost in the Middle: How Language Models Use Long Contexts", TACL 2024.
   https://arxiv.org/abs/2307.03172

6. Husain et al., "CodeSearchNet Challenge: Evaluating the State of Semantic Code Search", arXiv:1909.09436.
   https://arxiv.org/abs/1909.09436

7. Jain et al., "LLM Agents Improve Semantic Code Search", arXiv:2408.11058.
   https://arxiv.org/abs/2408.11058

8. Lee et al., "Shifting from Ranking to Set Selection for Retrieval Augmented Generation", arXiv:2507.06838.
   https://arxiv.org/abs/2507.06838
