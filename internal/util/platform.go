package util

import (
	"errors"
	"fmt"
	"runtime"
)

// ErrUnsupportedPlatform is returned by SupportedPlatform when the host OS
// or CPU architecture is outside dropzone's supported matrix:
// {linux, darwin} x {amd64, arm64}.
var ErrUnsupportedPlatform = errors.New("unsupported host platform")

// HostOS returns the current runtime GOOS ("linux" or "darwin").
func HostOS() string { return runtime.GOOS }

// HostArch returns the current runtime GOARCH ("amd64" or "arm64").
func HostArch() string { return runtime.GOARCH }

// HostPlatform returns the "os/arch" string used in OCI manifest lists.
func HostPlatform() string { return HostOS() + "/" + HostArch() }

// SupportedPlatform returns nil when the host is one of dropzone's four
// supported platforms, or an ErrUnsupportedPlatform-wrapped error otherwise.
func SupportedPlatform() error {
	os, arch := HostOS(), HostArch()
	if (os == "linux" || os == "darwin") && (arch == "amd64" || arch == "arm64") {
		return nil
	}
	return fmt.Errorf("%w: %s/%s (supported: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)", ErrUnsupportedPlatform, os, arch)
}
