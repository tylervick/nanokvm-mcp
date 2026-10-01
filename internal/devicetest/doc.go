// Package devicetest holds the opt-in smoke test that runs against real
// NanoKVM hardware.
//
// Everything here is behind a `//go:build device` tag, so `go test ./...` and
// CI compile this package to nothing — the directory reports "no test files".
// The test is run deliberately, against a device you have already deployed to:
//
//	mise run test-device
//
// See the Development section of README.md for the required environment and
// the SSH tunnel it assumes.
//
// The test drives the *deployed daemon* rather than constructing a client
// in-process, because the assertion that matters — resident memory under
// 25 MB — is a property of the riscv64 binary running on a device with ~43 MB
// free. Measuring a darwin/arm64 test binary would report a number unrelated
// to that budget.
package devicetest
