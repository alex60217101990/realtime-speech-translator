// CTranslate2 C ABI shim. Exposes the subset of the C++ Translator API
// the application needs: load a model directory, translate one batch of
// already-tokenized source pieces with an optional target prefix, free
// the result.
//
// All returned strings and arrays are caller-freeable.
#ifndef RST_CT2_CGO_SHIM_H
#define RST_CT2_CGO_SHIM_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ct2_translator ct2_translator;

// Construct a translator from a model directory. compute_type accepts
// "default", "int8", "int8_float32", "float16", "float32".
// device accepts "cpu" only for now; CUDA would be a separate build.
// num_threads <= 0 means CTranslate2 auto.
ct2_translator* ct2_new(
    const char* model_dir,
    const char* compute_type,
    int32_t num_threads,
    char** err);

void ct2_free(ct2_translator* t);

// Translate one source sentence (already tokenized into subword pieces).
// target_prefix may be NULL/0 to start decoding from scratch.
//
// On success returns 0; *out_pieces is a malloc'd array of *out_n
// nul-terminated UTF-8 strings (free via ct2_free_pieces).
// On failure returns 1 and *err is populated.
// repetition_penalty: >0 enables penalty (1.0 = neutral, typical 1.05-1.2).
//   <=0 means leave at CT2 default.
// no_repeat_ngram_size: >0 forbids n-gram repetition (typical 3). 0 disables.
// Both knobs short-circuit greedy decoder loops on weird inputs and
// usually shorten output length on noisy STT, so they cut wall time too.
int ct2_translate(
    ct2_translator* t,
    const char* const* source_pieces,
    int32_t n_source,
    const char* const* target_prefix,
    int32_t n_prefix,
    int32_t beam_size,
    int32_t max_decoding_length,
    float   repetition_penalty,
    int32_t no_repeat_ngram_size,
    char*** out_pieces,
    int32_t* out_n,
    char** err);

void ct2_free_pieces(char** pieces, int32_t n);
void ct2_free_str(char* s);

#ifdef __cplusplus
}
#endif

#endif
