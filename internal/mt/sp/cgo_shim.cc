// SentencePiece C ABI shim implementation. Links against
// libsentencepiece.a built by scripts/build-deps.sh.
#include "cgo_shim.h"

#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

#include "sentencepiece_processor.h"

extern "C" {

struct sp_processor {
    sentencepiece::SentencePieceProcessor proc;
};

static char* dup_c(const std::string& s) {
    char* p = static_cast<char*>(std::malloc(s.size() + 1));
    if (!p) return nullptr;
    std::memcpy(p, s.data(), s.size());
    p[s.size()] = '\0';
    return p;
}

sp_processor* sp_load(const char* model_path, char** err) {
    auto* p = new sp_processor();
    const auto status = p->proc.Load(model_path);
    if (!status.ok()) {
        if (err) *err = dup_c(status.ToString());
        delete p;
        return nullptr;
    }
    return p;
}

void sp_free(sp_processor* p) { delete p; }

int sp_encode(sp_processor* p, const char* text, int32_t** out_ids, int32_t* out_n, char** err) {
    if (!p || !text) return 1;
    std::vector<int> ids;
    const auto status = p->proc.Encode(text, &ids);
    if (!status.ok()) {
        if (err) *err = dup_c(status.ToString());
        return 1;
    }
    *out_n = static_cast<int32_t>(ids.size());
    *out_ids = static_cast<int32_t*>(std::malloc(sizeof(int32_t) * ids.size()));
    for (size_t i = 0; i < ids.size(); ++i) (*out_ids)[i] = ids[i];
    return 0;
}

int sp_decode(sp_processor* p, const int32_t* ids, int32_t n, char** out_text, char** err) {
    if (!p) return 1;
    std::vector<int> v(ids, ids + n);
    std::string out;
    const auto status = p->proc.Decode(v, &out);
    if (!status.ok()) {
        if (err) *err = dup_c(status.ToString());
        return 1;
    }
    *out_text = dup_c(out);
    return 0;
}

int32_t sp_piece_to_id(sp_processor* p, const char* piece) {
    if (!p || !piece) return -1;
    return p->proc.PieceToId(piece);
}

int sp_id_to_piece(sp_processor* p, int32_t id, char** out_text) {
    if (!p) return 1;
    *out_text = dup_c(p->proc.IdToPiece(id));
    return 0;
}

int32_t sp_vocab_size(sp_processor* p) { return p ? p->proc.GetPieceSize() : 0; }
int32_t sp_bos_id(sp_processor* p) { return p ? p->proc.bos_id() : -1; }
int32_t sp_eos_id(sp_processor* p) { return p ? p->proc.eos_id() : -1; }
int32_t sp_unk_id(sp_processor* p) { return p ? p->proc.unk_id() : -1; }
int32_t sp_pad_id(sp_processor* p) { return p ? p->proc.pad_id() : -1; }

int sp_encode_as_pieces(sp_processor* p, const char* text, char*** out_pieces, int32_t* out_n, char** err) {
    if (!p || !text) return 1;
    std::vector<std::string> pieces;
    const auto status = p->proc.Encode(text, &pieces);
    if (!status.ok()) {
        if (err) *err = dup_c(status.ToString());
        return 1;
    }
    *out_n = static_cast<int32_t>(pieces.size());
    *out_pieces = static_cast<char**>(std::malloc(sizeof(char*) * pieces.size()));
    for (size_t i = 0; i < pieces.size(); ++i) (*out_pieces)[i] = dup_c(pieces[i]);
    return 0;
}

int sp_decode_pieces(sp_processor* p, const char* const* pieces, int32_t n, char** out_text, char** err) {
    if (!p) return 1;
    std::vector<std::string> v;
    v.reserve(n);
    for (int32_t i = 0; i < n; ++i) v.emplace_back(pieces[i]);
    std::string out;
    const auto status = p->proc.Decode(v, &out);
    if (!status.ok()) {
        if (err) *err = dup_c(status.ToString());
        return 1;
    }
    *out_text = dup_c(out);
    return 0;
}

void sp_free_pieces(char** pieces, int32_t n) {
    if (!pieces) return;
    for (int32_t i = 0; i < n; ++i) std::free(pieces[i]);
    std::free(pieces);
}

void sp_free_ids(int32_t* ids) { std::free(ids); }
void sp_free_str(char* s) { std::free(s); }

}  // extern "C"
