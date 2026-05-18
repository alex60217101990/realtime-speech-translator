// CTranslate2 C ABI shim implementation. Links against
// libctranslate2.a (plus the ruy / cpu_features / spdlog satellites)
// built by scripts/build-deps.sh.
#include "cgo_shim.h"

#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

#include "ctranslate2/translator.h"
#include "ctranslate2/types.h"

extern "C" {

struct ct2_translator {
    ctranslate2::Translator* t;
};

static char* dup_c(const std::string& s) {
    char* p = static_cast<char*>(std::malloc(s.size() + 1));
    if (!p) return nullptr;
    std::memcpy(p, s.data(), s.size());
    p[s.size()] = '\0';
    return p;
}

static ctranslate2::ComputeType parse_compute_type(const char* s) {
    if (!s) return ctranslate2::ComputeType::DEFAULT;
    const std::string v = s;
    if (v == "int8")          return ctranslate2::ComputeType::INT8;
    if (v == "int8_float32")  return ctranslate2::ComputeType::INT8_FLOAT32;
    if (v == "int8_float16")  return ctranslate2::ComputeType::INT8_FLOAT16;
    if (v == "int8_bfloat16") return ctranslate2::ComputeType::INT8_BFLOAT16;
    if (v == "int16")         return ctranslate2::ComputeType::INT16;
    if (v == "float16")       return ctranslate2::ComputeType::FLOAT16;
    if (v == "float32")       return ctranslate2::ComputeType::FLOAT32;
    if (v == "bfloat16")      return ctranslate2::ComputeType::BFLOAT16;
    return ctranslate2::ComputeType::DEFAULT;
}

ct2_translator* ct2_new(
    const char* model_dir,
    const char* compute_type,
    int32_t num_threads,
    char** err) {

    try {
        ctranslate2::ReplicaPoolConfig pool_cfg;
        if (num_threads > 0) pool_cfg.num_threads_per_replica = static_cast<size_t>(num_threads);

        auto* w = new ct2_translator;
        w->t = new ctranslate2::Translator(
            std::string(model_dir),
            ctranslate2::Device::CPU,
            parse_compute_type(compute_type),
            std::vector<int>{0},
            /*tensor_parallel=*/false,
            pool_cfg);
        return w;
    } catch (const std::exception& e) {
        if (err) *err = dup_c(e.what());
        return nullptr;
    }
}

void ct2_free(ct2_translator* t) {
    if (!t) return;
    delete t->t;
    delete t;
}

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
    char** err) {

    if (!t || !t->t) {
        if (err) *err = dup_c("ct2: translator is null");
        return 1;
    }
    try {
        std::vector<std::string> src;
        src.reserve(n_source);
        for (int32_t i = 0; i < n_source; ++i) src.emplace_back(source_pieces[i]);

        std::vector<std::vector<std::string>> src_batch = { std::move(src) };

        ctranslate2::TranslationOptions opts;
        if (beam_size > 0) opts.beam_size = beam_size;
        if (max_decoding_length > 0) opts.max_decoding_length = max_decoding_length;
        if (repetition_penalty > 0.0f) opts.repetition_penalty = repetition_penalty;
        if (no_repeat_ngram_size > 0) opts.no_repeat_ngram_size = no_repeat_ngram_size;
        // Keep latency bounded — sampling stays at greedy by default.

        std::vector<ctranslate2::TranslationResult> results;
        if (n_prefix > 0 && target_prefix) {
            std::vector<std::string> prefix;
            prefix.reserve(n_prefix);
            for (int32_t i = 0; i < n_prefix; ++i) prefix.emplace_back(target_prefix[i]);
            std::vector<std::vector<std::string>> prefix_batch = { std::move(prefix) };
            results = t->t->translate_batch(src_batch, prefix_batch, opts);
        } else {
            results = t->t->translate_batch(src_batch, opts);
        }

        if (results.empty() || results[0].hypotheses.empty()) {
            if (err) *err = dup_c("ct2: empty translation result");
            return 1;
        }
        const auto& tokens = results[0].hypotheses[0];
        *out_n = static_cast<int32_t>(tokens.size());
        *out_pieces = static_cast<char**>(std::malloc(sizeof(char*) * tokens.size()));
        for (size_t i = 0; i < tokens.size(); ++i) (*out_pieces)[i] = dup_c(tokens[i]);
        return 0;
    } catch (const std::exception& e) {
        if (err) *err = dup_c(e.what());
        return 1;
    }
}

void ct2_free_pieces(char** pieces, int32_t n) {
    if (!pieces) return;
    for (int32_t i = 0; i < n; ++i) std::free(pieces[i]);
    std::free(pieces);
}

void ct2_free_str(char* s) { std::free(s); }

}  // extern "C"
