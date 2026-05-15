---
title: SentencePiece
date: 2026-05-15
tags:
  - library
  - mt
  - tokenizer
  - cgo
  - submodule
aliases:
  - sp
  - google/sentencepiece
---

# SentencePiece

Subword tokenizer used in front of [[CTranslate2]] for both [[../decisions/ADR-007 MADLAD over NLLB|MADLAD-400]] and OPUS-MT. Translates UTF-8 text to/from the integer-or-piece sequences that the neural model consumes.

## Vendoring

Submodule at `third_party/sentencepiece` pinned to **v0.2.1**.

## Build

```bash
cmake -S third_party/sentencepiece -B build_go \
  -DSPM_ENABLE_SHARED=OFF \
  -DSPM_USE_BUILTIN_PROTOBUF=ON \
  -DSPM_ENABLE_TCMALLOC=OFF \
  -DSPM_BUILD_TEST=OFF \
  -DCMAKE_POSITION_INDEPENDENT_CODE=ON
cmake --build build_go --target sentencepiece-static -j
```

**Target name is `sentencepiece-static`, not `sentencepiece`** — see [[../gotchas/CT2 Linker Symphony#Target naming]].

## Abseil gotcha

SentencePiece fetches abseil-cpp via `FetchContent` but does **not** bundle its objects into `libsentencepiece.a`. After the SP build, the cgo link fails on `absl::lts_...` symbols. Workaround in `scripts/build-deps.sh`:

```bash
cmake --build build_go --parallel        # build every absl_* target
# Merge libabsl_*.a files into one combined static lib
case $(uname -s) in
  Darwin) libtool -static -o build_go/src/libabsl_combined.a $absl_libs ;;
  *)      ar -M < absl_combined.mri ;;   # MRI script "create / addlib... / save / end"
esac
```

Cgo LDFLAGS then references `-labsl_combined` once instead of 90 individual libs. See [[../gotchas/SP Abseil Static Bundling]].

## Go wrapper API

```go
proc, _ := sp.Load("sentencepiece.bpe.model")

// Integer ids (e.g. for own training code)
ids, _ := proc.Encode("hello world")
text, _ := proc.Decode(ids)

// Pieces (subword strings) — required for CTranslate2 input
pieces, _ := proc.EncodePieces("<2en> привет")  // -> ["<2en>", "▁привет"]
text2, _ := proc.DecodePieces(pieces)            // -> "<2en> привет"

// Special tokens
bos := proc.BOSId()  // -1 if model has no <s> token
eos := proc.EOSId()
```

The shim exposes both `Encode` (ids) and `EncodePieces` (strings) because CTranslate2 needs pieces while some downstream code (token-level beam scoring, repetition penalty tuning) wants ids.

## Model file format

`.model` files are protobuf with the vocabulary, special tokens and the BPE/Unigram merge table. Distributed alongside CT2 model directories by upstream maintainers (e.g. `santhosh/madlad400-3b-ct2/sentencepiece.model`).

## Pitfalls

> [!warning] Abseil symbols missing
> Symptom: linker complains about `absl::lts_20260107::str_format_internal::...`. Cause: SP `.a` doesn't contain abseil. Fix: build all `absl_*` targets and link the merged combined lib.

> [!warning] Different model for source vs target
> OPUS-MT ships two `.spm` files per direction (`source.spm`, `target.spm`). MADLAD-400 ships one shared model. The Engine implementations track this — see [[../../../internal/mt/opusmt.go]] vs [[../../../internal/mt/madlad.go]].

## See also

- Repo: https://github.com/google/sentencepiece
- Wrapper: [[../../../internal/mt/sp/sp.go]]
- Shim: [[../../../internal/mt/sp/cgo_shim.cc]]
- [[CTranslate2]]
- [[../gotchas/SP Abseil Static Bundling]]
