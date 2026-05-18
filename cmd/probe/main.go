// Command probe is a minimal smoke test that the sherpa-onnx-go module
// linked correctly and the bundled native library loads on this
// platform. It prints the runtime version reported by the C-API and
// exits.
//
// Usage:
//
//	go run ./cmd/probe
//
// Expected output (example):
//
//	sherpa-onnx C-API version: 1.12.38
package main

import (
	"fmt"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

func main() {
	fmt.Printf("sherpa-onnx C-API version: %s\n", sherpa.GetVersion())
}
