package buildinfo

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

const (
	Product            = "ClusterGuard HA"
	StateFormat        = 1
	UpdateProtocol     = 1
	UpdateGateProtocol = 1

	// DevRelease is the release a binary reports when the release builder did not
	// link one in. It names no real release line, so anything that has to compare
	// release lines must treat it as "unknown" rather than as a match candidate.
	DevRelease = "0"
)

// These values are replaced by the release builder through Go linker flags.
// Development builds deliberately keep non-empty values so every binary still
// exposes a complete compatibility contract.
var (
	Version = "development"
	Release = "0"
	Commit  = "unknown"
	BuiltAt = "unknown"
	// ProductVersion is the sealed four-part runtime identity. Version/Release
	// remain the RPM compatibility baseline consumed by existing upgrade tools.
	ProductVersion = ""
)

type Info struct {
	Product            string `json:"product"`
	Binary             string `json:"binary"`
	Version            string `json:"version"`
	Release            string `json:"release"`
	ProductVersion     string `json:"product_version,omitempty"`
	Commit             string `json:"commit"`
	BuiltAt            string `json:"built_at"`
	OS                 string `json:"os"`
	Architecture       string `json:"architecture"`
	RPMArchitecture    string `json:"rpm_architecture"`
	StateFormat        int    `json:"state_format"`
	UpdateProtocol     int    `json:"update_protocol"`
	UpdateGateProtocol int    `json:"update_gate_protocol"`
}

func Current(binary string) Info {
	productVersion := ProductVersion
	if !ValidProductVersion(productVersion) {
		productVersion = ""
	}
	return Info{
		Product:            Product,
		Binary:             binary,
		Version:            Version,
		Release:            Release,
		ProductVersion:     productVersion,
		Commit:             Commit,
		BuiltAt:            BuiltAt,
		OS:                 runtime.GOOS,
		Architecture:       runtime.GOARCH,
		RPMArchitecture:    RPMArchitecture(runtime.GOARCH),
		StateFormat:        StateFormat,
		UpdateProtocol:     UpdateProtocol,
		UpdateGateProtocol: UpdateGateProtocol,
	}
}

var productVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$`)

func ValidProductVersion(value string) bool {
	if !productVersionPattern.MatchString(value) {
		return false
	}
	parts := strings.Split(value, ".")
	return strings.TrimLeft(parts[3], "0") != ""
}

func (info Info) DisplayVersion() string {
	if ValidProductVersion(info.ProductVersion) {
		return info.ProductVersion
	}
	if info.Release == "" || info.Release == DevRelease {
		return info.Version
	}
	return info.Version + "-" + info.Release
}

// VersionLine is the single-line identity every payload binary reports. The
// release builder links the same four-part product identity into all of them,
// so an operator can ask any binary on a node which product build it is. A
// linker flag alone is not enough: the Go linker drops a variable nothing
// references, so every command has to report the identity to keep it compiled
// into the artifact.
func VersionLine(binary string) string {
	info := Current(binary)
	return fmt.Sprintf("%s %s (%s, state-format=%d, update-protocol=%d)",
		info.Product, info.DisplayVersion(), info.Commit, info.StateFormat, info.UpdateProtocol)
}

func RPMArchitecture(architecture string) string {
	switch architecture {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i386"
	default:
		return architecture
	}
}
