//go:build !unix

package safefile

// checkModes says whether the platform has the mode bits that [TooOpen] reads. A
// platform without them (Windows keeps access in ACLs) is not checked.
const checkModes = false
