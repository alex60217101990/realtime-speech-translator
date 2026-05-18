package uihost

import "unsafe"

// unsafePtrOf is a one-line helper so api.go can stay free of
// "unsafe" import noise at the top. The cast is identical to what
// the math package's Float32bits implementation uses internally.
func unsafePtrOf(p any) unsafe.Pointer {
	switch v := p.(type) {
	case *float32:
		return unsafe.Pointer(v)
	case *uint32:
		return unsafe.Pointer(v)
	}
	panic("uihost: unsupported pointer type in unsafePtrOf")
}
