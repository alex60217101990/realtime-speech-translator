---
title: "ADR-007: MADLAD-400 default over NLLB-200"
date: 2026-05-15
tags:
  - decision
  - adr
  - accepted
  - mt
  - licensing
status: accepted
deciders: alex60217101990
---

# ADR-007: MADLAD-400 default over NLLB-200

## Status

**Accepted** — 2026-05-15. Supersedes the NLLB-200 default tentatively proposed in [[../../wiki/03-Models]] and [[../../wiki/10-Licensing-Distribution]].

## Context

The architecture wiki listed **NLLB-200-distilled-600M** as the default MT model on the strength of quality and language coverage. NLLB-200 is released under **CC-BY-NC-4.0**, which forbids commercial use. That license:

- Blocks shipping a paid version of the translator.
- Blocks bundling for an employer / contract work.
- Triggers attribution + share-alike obligations the average user must read.

We surveyed alternatives ([[../../docs/wiki/10-Licensing-Distribution]] table) and asked the user which trade-off to take.

## Options reviewed

| Model | License | BLEU vs NLLB-600M | RAM (int8) | Latency CPU | Langs |
|---|---|---|---|---|---|
| **MADLAD-400-3B** | **Apache-2.0** | **+2 to +4** | 1.6 GB | 600–900 ms | **419** |
| MADLAD-400-7B | Apache-2.0 | +5 to +7 | 3.5 GB | 1.5–2.5 s (CPU borderline) | 419 |
| M2M-100-418M | MIT | −3 to −5 | 500 MB | 250–400 ms | 100 |
| OPUS-MT per-pair | Apache-2.0 / MIT | varies | 150–300 MB / pair | 100–200 ms | per-pair |
| NLLB-200-distilled-600M | CC-BY-NC | 0 (baseline) | 650 MB | 200–400 ms | 200 |
| SeamlessM4T | CC-BY-NC | +5 | 2.4 GB | not CPU realtime | 100 |

## Decision

1. **Default backend**: MADLAD-400-3B int8 via CTranslate2. Apache-2.0, modern (Google 2023), best quality among permissively-licensed models, 419 languages, fits the latency budget on M-series and Ryzen 5+ class CPUs.
2. **Lightweight fallback**: OPUS-MT (Helsinki-NLP, Apache-2.0). Per-pair models 150–300 MB each. User picks pairs in Models Manager; appropriate for low-RAM machines or strict-latency scenarios.
3. **NLLB-200 removed** as the default. Not added as a manifest entry — bundling would require non-commercial badges throughout the UI.

The pipeline picks the backend at session start; [[../../../internal/mt/mt.go]] is a stable interface, [[../../../internal/mt/madlad.go]] and [[../../../internal/mt/opusmt.go]] are the two implementations.

## Consequences

Positive:

- License is unambiguous: ship and sell freely under Apache-2.0.
- 419 languages is a strict superset of NLLB-200's 200 — broader practical coverage.
- Same CTranslate2 + SentencePiece infra serves both backends, so swap costs are low.

Negative:

- MADLAD-3B int8 is ~1.6 GB on disk vs NLLB-600M's ~650 MB. First-run download is heavier.
- Latency 1.5–2× higher than NLLB on the same CPU. End-to-end p95 budget tightens but still within the < 2 s target with whisper-small + Piper-medium.
- We expose a manifest entry for **M2M-100-418M** (MIT, smaller) as an additional fallback for slow machines; user picks in the Models Manager.

## Revisit when

- A better-licensed model (Apache-2.0/MIT) substantially beats MADLAD-400-3B on BLEU and fits the same latency budget.
- Hardware becomes commonly powerful enough to run MADLAD-7B at real-time (~3.5 GB).
- A formally non-fine-tuned NLLB-equivalent is released under permissive terms.

## See also

- [[../libraries/CTranslate2]]
- [[../libraries/SentencePiece]]
- [[../../wiki/03-Models]]
- [[../../wiki/10-Licensing-Distribution]]
