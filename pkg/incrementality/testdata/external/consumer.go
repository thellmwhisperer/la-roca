package incrementalityconsumer

import "github.com/thellmwhisperer/la-roca/pkg/incrementality"

var (
	_ = incrementality.Fingerprint
	_ = incrementality.MetadataFingerprint
	_ = incrementality.TargetFingerprint
	_ = incrementality.LoadState
	_ = incrementality.Unchanged
	_ = incrementality.UnchangedMetadata
	_ = incrementality.RecordState
)

var (
	_ incrementality.Target
	_ incrementality.FileState
)
