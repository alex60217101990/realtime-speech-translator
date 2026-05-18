// SentencePiece C ABI shim. Exposes the subset of the C++ Processor
// API that cgo needs: load a .model file, encode a UTF-8 string into a
// caller-owned array of int32 token ids, and decode the reverse.
//
// All allocations are caller-freeable via the sp_free_* functions.
#ifndef RST_SP_CGO_SHIM_H
#define RST_SP_CGO_SHIM_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct sp_processor sp_processor;

// Returns NULL on failure; *err is set to a malloc'd UTF-8 string the
// caller must release with sp_free_str.
sp_processor* sp_load(const char* model_path, char** err);
void sp_free(sp_processor* p);

// Encode encodes UTF-8 text into token ids. *out_ids points at a
// newly-malloc'd int32_t array of length *out_n. Returns 0 on success,
// 1 on failure with *err populated.
int sp_encode(sp_processor* p, const char* text, int32_t** out_ids, int32_t* out_n, char** err);

// Decode decodes ids back to UTF-8. *out_text points at a newly-malloc'd
// nul-terminated string. Returns 0 on success.
int sp_decode(sp_processor* p, const int32_t* ids, int32_t n, char** out_text, char** err);

// EncodeAsPieces tokenizes text into subword piece strings. Pieces are
// CTranslate2 translator inputs (a model_dir-resolved vocabulary maps
// them to embeddings). On success *out_pieces is a malloc'd array of
// nul-terminated UTF-8 strings of length *out_n; both array and elements
// must be freed via sp_free_pieces.
int sp_encode_as_pieces(sp_processor* p, const char* text, char*** out_pieces, int32_t* out_n, char** err);

// DecodePieces is the inverse — concatenates subword pieces into a UTF-8
// string with SentencePiece's normalization.
int sp_decode_pieces(sp_processor* p, const char* const* pieces, int32_t n, char** out_text, char** err);

void sp_free_pieces(char** pieces, int32_t n);

// Resolve a token piece (e.g. "<s>") to its integer id. Returns -1 if
// unknown.
int32_t sp_piece_to_id(sp_processor* p, const char* piece);
int sp_id_to_piece(sp_processor* p, int32_t id, char** out_text);

int32_t sp_vocab_size(sp_processor* p);
int32_t sp_bos_id(sp_processor* p);
int32_t sp_eos_id(sp_processor* p);
int32_t sp_unk_id(sp_processor* p);
int32_t sp_pad_id(sp_processor* p);

void sp_free_ids(int32_t* ids);
void sp_free_str(char* s);

#ifdef __cplusplus
}
#endif

#endif
