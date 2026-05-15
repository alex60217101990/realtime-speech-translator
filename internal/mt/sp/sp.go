// Package sp wraps SentencePiece tokenization via a thin C++ shim.
//
// The model file is the binary `.model` produced by `spm_train` or the
// one distributed by upstream tokenizer authors (e.g. the
// `sentencepiece.bpe.model` from facebook/nllb-200-distilled-600M).
package sp

/*
#cgo CXXFLAGS: -std=c++17 -O2
#cgo CPPFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -lsentencepiece -labsl_combined -lstdc++
#include "cgo_shim.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// Processor wraps a loaded SentencePiece model.
type Processor struct {
	c *C.sp_processor
}

// Load reads a .model file from disk.
func Load(path string) (*Processor, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	var cerr *C.char
	h := C.sp_load(cpath, &cerr)
	if h == nil {
		msg := "sentencepiece: load failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.sp_free_str(cerr)
		}
		return nil, errors.New(msg)
	}
	p := &Processor{c: h}
	runtime.SetFinalizer(p, func(p *Processor) { _ = p.Close() })
	return p, nil
}

// Close releases the underlying C++ Processor.
func (p *Processor) Close() error {
	if p.c != nil {
		C.sp_free(p.c)
		p.c = nil
	}
	return nil
}

// Encode tokenizes UTF-8 text into the model's vocabulary ids.
func (p *Processor) Encode(text string) ([]int32, error) {
	if p.c == nil {
		return nil, errors.New("sentencepiece: closed")
	}
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))

	var ids *C.int32_t
	var n C.int32_t
	var cerr *C.char
	if C.sp_encode(p.c, ctext, &ids, &n, &cerr) != 0 {
		msg := "sentencepiece: encode failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.sp_free_str(cerr)
		}
		return nil, errors.New(msg)
	}
	defer C.sp_free_ids(ids)

	out := make([]int32, int(n))
	src := unsafe.Slice((*int32)(unsafe.Pointer(ids)), int(n))
	copy(out, src)
	return out, nil
}

// Decode rejoins token ids into UTF-8 text.
func (p *Processor) Decode(ids []int32) (string, error) {
	if p.c == nil {
		return "", errors.New("sentencepiece: closed")
	}
	var (
		cids *C.int32_t
		n    C.int32_t = C.int32_t(len(ids))
	)
	if len(ids) > 0 {
		cids = (*C.int32_t)(unsafe.Pointer(&ids[0]))
	}

	var cout *C.char
	var cerr *C.char
	if C.sp_decode(p.c, cids, n, &cout, &cerr) != 0 {
		msg := "sentencepiece: decode failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.sp_free_str(cerr)
		}
		return "", errors.New(msg)
	}
	defer C.sp_free_str(cout)
	return C.GoString(cout), nil
}

// EncodePieces tokenizes text into subword piece strings. CTranslate2
// consumes pieces directly (it looks them up in the model's embedded
// vocabulary).
func (p *Processor) EncodePieces(text string) ([]string, error) {
	if p.c == nil {
		return nil, errors.New("sentencepiece: closed")
	}
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))

	var pieces **C.char
	var n C.int32_t
	var cerr *C.char
	if C.sp_encode_as_pieces(p.c, ctext, &pieces, &n, &cerr) != 0 {
		msg := "sentencepiece: encode_pieces failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.sp_free_str(cerr)
		}
		return nil, errors.New(msg)
	}
	defer C.sp_free_pieces(pieces, n)

	out := make([]string, int(n))
	if pieces != nil && n > 0 {
		raw := unsafe.Slice(pieces, int(n))
		for i := range out {
			out[i] = C.GoString(raw[i])
		}
	}
	return out, nil
}

// DecodePieces is the inverse of EncodePieces: it rejoins piece
// strings into UTF-8 text using SentencePiece's normalization.
func (p *Processor) DecodePieces(pieces []string) (string, error) {
	if p.c == nil {
		return "", errors.New("sentencepiece: closed")
	}
	cs := make([]*C.char, len(pieces))
	for i, s := range pieces {
		cs[i] = C.CString(s)
	}
	defer func() {
		for _, p := range cs {
			C.free(unsafe.Pointer(p))
		}
	}()

	var arr **C.char
	if len(cs) > 0 {
		arr = (**C.char)(unsafe.Pointer(&cs[0]))
	}

	var out *C.char
	var cerr *C.char
	if C.sp_decode_pieces(p.c, arr, C.int32_t(len(pieces)), &out, &cerr) != 0 {
		msg := "sentencepiece: decode_pieces failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.sp_free_str(cerr)
		}
		return "", errors.New(msg)
	}
	defer C.sp_free_str(out)
	return C.GoString(out), nil
}

// PieceToId resolves a literal piece (e.g. "<s>", "▁hello") to its id.
// Returns -1 if the piece is not in the vocabulary.
func (p *Processor) PieceToId(piece string) int32 {
	if p.c == nil {
		return -1
	}
	cp := C.CString(piece)
	defer C.free(unsafe.Pointer(cp))
	return int32(C.sp_piece_to_id(p.c, cp))
}

// IdToPiece returns the surface form of a token id.
func (p *Processor) IdToPiece(id int32) string {
	if p.c == nil {
		return ""
	}
	var out *C.char
	if C.sp_id_to_piece(p.c, C.int32_t(id), &out) != 0 || out == nil {
		return ""
	}
	defer C.sp_free_str(out)
	return C.GoString(out)
}

// VocabSize returns the number of tokens in the loaded model.
func (p *Processor) VocabSize() int32 {
	if p.c == nil {
		return 0
	}
	return int32(C.sp_vocab_size(p.c))
}

// BOSId, EOSId, UNKId, PADId return the model's special-token ids.
//
// Each delegates to a separate cgo wrapper: cgo cannot take the address
// of a C function and pass it through a Go function value, so we cannot
// share an idOr helper.
func (p *Processor) BOSId() int32 {
	if p == nil || p.c == nil {
		return -1
	}
	return int32(C.sp_bos_id(p.c))
}

func (p *Processor) EOSId() int32 {
	if p == nil || p.c == nil {
		return -1
	}
	return int32(C.sp_eos_id(p.c))
}

func (p *Processor) UNKId() int32 {
	if p == nil || p.c == nil {
		return -1
	}
	return int32(C.sp_unk_id(p.c))
}

func (p *Processor) PADId() int32 {
	if p == nil || p.c == nil {
		return -1
	}
	return int32(C.sp_pad_id(p.c))
}
