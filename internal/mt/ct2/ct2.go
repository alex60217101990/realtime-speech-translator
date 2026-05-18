//go:build mt

// Package ct2 wraps the CTranslate2 inference runtime via a thin C++
// shim. It exposes only what the application needs: load a model
// directory, run translate_batch on a single sentence (with optional
// target prefix), receive subword pieces as output.
//
// Build tag `mt` gates this package because CTranslate2 must be
// compiled from third_party/ctranslate2 first
// (scripts/build-deps.sh ctranslate2). Without -tags mt the
// translator runs with the no-op mt.Disabled engine instead.
//
// Tokenization is intentionally not done here — see internal/mt/sp.
package ct2

/*
#cgo CXXFLAGS: -std=c++17 -O2
#cgo CPPFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -lctranslate2 -lcpu_features
#cgo LDFLAGS: -lruy_frontend -lruy_context -lruy_context_get_ctx -lruy_ctx
#cgo LDFLAGS: -lruy_trmul -lruy_thread_pool -lruy_blocking_counter -lruy_wait
#cgo LDFLAGS: -lruy_block_map -lruy_allocator -lruy_prepacked_cache -lruy_cpuinfo
#cgo LDFLAGS: -lruy_kernel_arm -lruy_kernel_avx -lruy_kernel_avx2_fma -lruy_kernel_avx512
#cgo LDFLAGS: -lruy_pack_arm -lruy_pack_avx -lruy_pack_avx2_fma -lruy_pack_avx512
#cgo LDFLAGS: -lruy_apply_multiplier -lruy_prepare_packed_matrices
#cgo LDFLAGS: -lruy_have_built_path_for_avx -lruy_have_built_path_for_avx2_fma -lruy_have_built_path_for_avx512
#cgo LDFLAGS: -lruy_denormal -lruy_tune -lruy_system_aligned_alloc
#cgo LDFLAGS: -lcpuinfo -lclog
#cgo darwin LDFLAGS: -framework Accelerate
#cgo linux LDFLAGS: -lopenblas
#cgo LDFLAGS: -lstdc++
#include "cgo_shim.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// ComputeType selects the inference precision. INT8 is the default and
// best balance for CPU MADLAD/NLLB/OPUS-MT.
type ComputeType string

const (
	ComputeDefault     ComputeType = ""
	ComputeInt8        ComputeType = "int8"
	ComputeInt8Float32 ComputeType = "int8_float32"
	ComputeFloat32     ComputeType = "float32"
	ComputeFloat16     ComputeType = "float16"
)

// Options configure a Translator at construction time.
type Options struct {
	ComputeType ComputeType
	// Threads is the per-replica thread count. Zero means CTranslate2
	// auto (typically NumCPU).
	Threads int
}

// Translator is a loaded CT2 model ready to translate.
type Translator struct {
	c *C.ct2_translator
}

// New loads a CTranslate2 model directory.
//
// When opts.Threads is 0 we resolve it to runtime.NumCPU() rather than
// leaving the "auto" default, because CTranslate2's auto-detect is
// conservative on x86 macOS and was leaving cores idle during MADLAD
// 3B int8 inference — the single biggest contributor to perceived lag
// on this stack.
func New(modelDir string, opts Options) (*Translator, error) {
	cdir := C.CString(modelDir)
	defer C.free(unsafe.Pointer(cdir))

	cct := C.CString(string(opts.ComputeType))
	defer C.free(unsafe.Pointer(cct))

	threads := opts.Threads
	if threads <= 0 {
		threads = runtime.NumCPU()
	}
	var cerr *C.char
	h := C.ct2_new(cdir, cct, C.int32_t(threads), &cerr)
	if h == nil {
		msg := "ct2: load failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.ct2_free_str(cerr)
		}
		return nil, errors.New(msg)
	}
	t := &Translator{c: h}
	runtime.SetFinalizer(t, func(t *Translator) { _ = t.Close() })
	return t, nil
}

// Close releases the model.
func (t *Translator) Close() error {
	if t.c != nil {
		C.ct2_free(t.c)
		t.c = nil
	}
	return nil
}

// TranslateOptions are per-call decoding controls.
type TranslateOptions struct {
	BeamSize           int
	MaxDecodingLength  int
	TargetPrefixPieces []string
}

// Translate runs translate_batch on a single sentence already tokenized
// into subword pieces. It returns the target-side pieces; rejoin them
// via sp.Processor.DecodePieces (or sp.Decode for ids variant).
func (t *Translator) Translate(sourcePieces []string, opt TranslateOptions) ([]string, error) {
	if t.c == nil {
		return nil, errors.New("ct2: closed")
	}
	if len(sourcePieces) == 0 {
		return nil, errors.New("ct2: empty source")
	}

	srcCStrs := newCStringSlice(sourcePieces)
	defer freeCStringSlice(srcCStrs)

	var prefixCStrs []*C.char
	if len(opt.TargetPrefixPieces) > 0 {
		prefixCStrs = newCStringSlice(opt.TargetPrefixPieces)
		defer freeCStringSlice(prefixCStrs)
	}

	srcPtr := firstElem(srcCStrs)
	prefixPtr := firstElem(prefixCStrs)

	var (
		outArr **C.char
		outN   C.int32_t
		cerr   *C.char
	)
	rc := C.ct2_translate(
		t.c,
		srcPtr,
		C.int32_t(len(sourcePieces)),
		prefixPtr,
		C.int32_t(len(prefixCStrs)),
		C.int32_t(opt.BeamSize),
		C.int32_t(opt.MaxDecodingLength),
		&outArr,
		&outN,
		&cerr,
	)
	if rc != 0 {
		msg := "ct2: translate failed"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.ct2_free_str(cerr)
		}
		return nil, errors.New(msg)
	}
	defer C.ct2_free_pieces(outArr, outN)

	out := make([]string, int(outN))
	if outArr != nil && outN > 0 {
		rawPtrs := unsafe.Slice(outArr, int(outN))
		for i := range out {
			out[i] = C.GoString(rawPtrs[i])
		}
	}
	return out, nil
}

// newCStringSlice C-mallocs each string in `in`. The returned slice owns
// the C pointers; pass it to freeCStringSlice when done.
//
// We deliberately keep the *C.char values in a contiguous Go slice so
// that taking the address of element zero yields a `char**` view of the
// whole array — required by the C ABI of ct2_translate.
func newCStringSlice(in []string) []*C.char {
	if len(in) == 0 {
		return nil
	}
	cs := make([]*C.char, len(in))
	for i, s := range in {
		cs[i] = C.CString(s)
	}
	return cs
}

func freeCStringSlice(cs []*C.char) {
	for _, p := range cs {
		if p != nil {
			C.free(unsafe.Pointer(p))
		}
	}
}

func firstElem(cs []*C.char) **C.char {
	if len(cs) == 0 {
		return nil
	}
	return (**C.char)(unsafe.Pointer(&cs[0]))
}
