package controlstate

const (
	// MaximumBytes is the absolute decode and transport boundary. It leaves a
	// bounded upgrade path for snapshots written before history retention was
	// introduced.
	MaximumBytes = 64 << 20
	// MaximumEncodedBytes keeps all newly committed snapshots comfortably below
	// the absolute boundary so replication and restore retain operating margin.
	MaximumEncodedBytes = 16 << 20
)
