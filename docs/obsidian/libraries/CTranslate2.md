---
title: CTranslate2
date: 2026-05-15
tags:
  - library
  - mt
  - cgo
  - submodule
aliases:
  - ct2
  - OpenNMT/CTranslate2
---

# CTranslate2

Fast inference runtime for Transformer translation models. The actual MT engine behind both [[../decisions/ADR-007 MADLAD over NLLB|MADLAD-400]] and [[../decisions/ADR-007 MADLAD over NLLB|OPUS-MT]] backends in this project.

## Vendoring

Submodule at `third_party/ctranslate2` pinned to **v4.7.1**. Nested submodules (spdlog, thrust, cub, ruy, cpu_features) initialised via `git submodule update --init --recursive` from the parent on clone.

## Build wiring

Built as a single static archive via [[../../../scripts/build-deps.sh|scripts/build-deps.sh]]:

```bash
cmake -S third_party/ctranslate2 -B build_go \
  -DBUILD_SHARED_LIBS=OFF \
  -DWITH_MKL=OFF -DWITH_DNNL=OFF -DWITH_CUDA=OFF \
  -DWITH_RUY=ON -DOPENMP_RUNTIME=NONE \
  -DCMAKE_POLICY_VERSION_MINIMUM=3.5      # cpu_features needs this
cmake --build build_go --target ctranslate2 -j
```

Produces `libctranslate2.a` plus 28+ ruy satellite libs (`libruy_*.a`), `libcpu_features.a`, and ruy's bundled `libcpuinfo.a` / `libclog.a`. **All must appear in the cgo `LDFLAGS`** or the linker errors out — see [[../gotchas/CT2 Linker Symphony]].

## Go cgo wrapper

Shim files: [[../../../internal/mt/ct2/cgo_shim.h]] + [[../../../internal/mt/ct2/cgo_shim.cc]]. Go: [[../../../internal/mt/ct2/ct2.go]].

Exposed surface is intentionally minimal:

```go
t, err := ct2.New(modelDir, ct2.Options{ComputeType: ct2.ComputeInt8})
pieces, err := t.Translate(srcPieces, ct2.TranslateOptions{BeamSize: 1, MaxDecodingLength: 256})
```

`srcPieces` is `[]string` of SentencePiece subword tokens (e.g. `["▁Hello", "▁world", "."]`). CT2 internally maps them to embedding indices using the vocabulary file shipped in `modelDir`. Tokenisation is **not** in CT2 — see [[SentencePiece]].

## Compute types

| Type | Quality | Speed (CPU) | Memory |
|---|---|---|---|
| `float32` | reference | slow | 2× model size |
| `int8_float32` | -0.5 BLEU | 1.5–2× faster | 0.5× model size |
| **`int8`** | -0.5–1 BLEU | fastest CPU | 0.5× model size, **default** |
| `int8_bfloat16` | similar to int8 | medium | — |
| `float16` | reference | needs AVX-512 BF16 | medium |

MADLAD-400-3B `int8` ≈ 1.6 GB on disk and roughly the same in RAM.

## Threading

`ReplicaPoolConfig.num_threads_per_replica` defaults to 0 = auto = `std::thread::hardware_concurrency()`. We expose this via `ct2.Options.Threads`; sensible default is `runtime.NumCPU() / 2` so the audio thread is not starved.

CT2's outer `Translator` allows multiple replicas (parallel batches) — we use 1 replica (single device, single index). Multiple replicas would help in batched server scenarios; for our one-utterance-at-a-time pipeline a single replica is correct.

## Model format

CT2 has its own binary tensor format. Convert via the upstream Python CLI:

```bash
pip install ctranslate2 transformers sentencepiece
ct2-transformers-converter --model jbochi/madlad400-3b-mt \
  --output_dir madlad-ct2-int8 --quantization int8
```

We ship a ready-converted variant in the manifest (see [[../../../internal/models/manifest/manifest.yaml]]).

## Pitfalls

> [!warning] `CMAKE_POLICY_VERSION_MINIMUM=3.5` required
> The bundled `third_party/cpu_features` declares `cmake_minimum_required(VERSION 3.5)` which CMake ≥ 4 rejects without an explicit policy override. Without this flag the configure step fails.

> [!warning] `nlohmann/json.hpp` and `half_float/half.hpp` warnings
> CT2 vendors these headers and they trigger `-Wdeprecated-literal-operator` on modern clang. Silence in CGO_CXXFLAGS — see [[../gotchas/CT2 Linker Symphony#Warning noise]].

## See also

- Repo: https://github.com/OpenNMT/CTranslate2
- C++ API: `third_party/ctranslate2/include/ctranslate2/translator.h`
- [[SentencePiece]]
- [[../decisions/ADR-007 MADLAD over NLLB]]
- [[../gotchas/CT2 Linker Symphony]]
