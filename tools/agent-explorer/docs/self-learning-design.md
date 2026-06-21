# Self-Learning Design

Dokumen ini menjelaskan bentuk `self-learning` yang dipakai `agent-explorer` agar tetap generic, bisa dipakai lintas repo, dan tidak berubah jadi policy liar.

## Prinsip

`self-learning` di runtime bukan berarti model mengubah bobotnya sendiri.

Bentuk yang dipakai di sini:
- memory pengalaman sukses
- memory false positive
- memory jalur evidence yang pernah benar
- reuse memory itu untuk anchor selection dan reranking

## Kenapa bentuk ini

Alasan praktis:
- murah
- cepat
- bisa diaudit
- bisa dihapus / rollback
- tidak mengikat satu project

Alasan dari literatur:
- retrieval repository-level butuh multi-stage reranking, bukan satu kali search mentah
- graph retrieval membantu multi-hop reasoning, tapi tetap perlu pembatas scope
- agent memory paling aman dipakai sebagai pengalaman terstruktur, bukan update model langsung

## Referensi arah

1. Anthropic, *Effective context engineering for AI agents*
   - context harus kecil, relevan, dan just-in-time
   - retrieval loop perlu buang noise sedini mungkin

2. Carnegie Mellon, *Repository-level Code Search with Neural Retrieval Methods*
   - repo-level code search efektif saat memakai multi-stage reranking
   - satu retrieval pass sering tidak cukup

3. *SAGE: Self-evolving Agents with Reflective and Memory-augmented Abilities*
   - refleksi + memory lebih stabil daripada berharap model “belajar sendiri” saat runtime

4. *A-Mem: Agentic Memory for LLM Agents*
   - memory paling berguna saat diorganisir sebagai pengalaman yang bisa diambil lagi untuk task mirip

5. *Graph-based Agent Memory: Taxonomy, Techniques, and Applications*
   - graph/structured memory cocok untuk akumulasi pengalaman, feedback, dan reuse reasoning path

## Implementasi sekarang

Saat ini `agent-explorer` menyimpan:
- accepted paths
- rejected paths
- accepted symbols
- rejected symbols
- topic -> accepted path frequency

Sumber masuk memory sekarang:
- `eval --auto-learn` dari suite repo nyata
- `feedback` manual untuk admin/debug

Lokasi default:

```text
/var/pile/agent-explorer/data/<repo-slug>/feedback.jsonl
```

## Efek ke runtime

Memory dipakai untuk:
- menaikkan skor hit pada path/symbol yang pernah diterima
- menurunkan skor hit pada path/symbol yang pernah ditolak
- menambah prior anchor dari topic yang pernah sukses

Ini sengaja dibatasi:
- tidak auto-edit prompt global
- tidak auto-ubah tool policy
- tidak auto-fine-tune model
- tidak minta main agent mengirim accepted/rejected di tiap panggilan `ask`

## Common pitfalls

1. Overfit ke satu repo
   - hindari hardcode domain/path di core
   - simpan repo-specific experience di memory repo itu sendiri

2. Feedback terlalu kasar
   - path rejection umum seperti `services/` akan merusak banyak query
   - pakai path/symbol spesifik

3. Policy mutation otomatis
   - lebih berisiko daripada memory rerank
   - rawan drift dan susah rollback

4. Tidak bedakan accepted vs rejected
   - success memory saja tidak cukup
   - false positive memory penting untuk recall yang bersih

5. Memory tanpa audit
   - memory harus bisa dibaca, dibersihkan, dan dijelaskan

## Tahap berikutnya

Urutan upgrade yang sehat:

1. accepted/rejected pairs
2. query exemplar store
3. LLM critic di atas top candidate
4. repo-specific critique log
5. baru policy adaptation yang lebih otomatis

## Batas jujur

Desain ini belum membuat agent “belajar” seperti training model.

Yang dilakukan:
- adaptasi ranking
- adaptasi anchor prior
- reuse pengalaman eksplorasi
- auto-ingest hasil eval agar loop belajar tetap internal

Itu cukup untuk membuat explorer terasa makin pintar lintas sesi tanpa mengorbankan stabilitas.
