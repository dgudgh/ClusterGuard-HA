package buildinfo

import "runtime"

const (
	Product            = "ClusterGuard HA"
	StateFormat        = 1
	UpdateProtocol     = 1
	UpdateGateProtocol = 1
)

// These values are replaced by the release builder through Go linker flags.
// Development builds deliberately keep non-empty values so every binary still
// exposes a complete compatibility contract.
var (
	Version = "development"
	Release = "0"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

type Info struct {
	Product            string `json:"product"`
	Binary             string `json:"binary"`
	Version            string `json:"version"`
	Release            string `json:"release"`
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
	return Info{
		Product:            Product,
		Binary:             binary,
		Version:            Version,
		Release:            Release,
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
