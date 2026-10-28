// Package slskd is a client for the slskd REST API (api/v0).
//
// Endpoints and field names were verified against the slskd source
// (src/slskd/Search/API, src/slskd/Transfers/API) and Soulseek.NET enums.
package slskd

import (
	"encoding/json"
	"strconv"
	"strings"
)

// File is a single file in a search response.
type File struct {
	Filename          string `json:"filename"`
	Size              int64  `json:"size"`
	Extension         string `json:"extension"`
	BitRate           *int   `json:"bitRate"`
	SampleRate        *int   `json:"sampleRate"`
	BitDepth          *int   `json:"bitDepth"`
	Length            *int   `json:"length"` // seconds
	IsVariableBitRate *bool  `json:"isVariableBitRate"`
	IsLocked          bool   `json:"isLocked"`
}

// Response is one peer's answer to a search.
type Response struct {
	Username          string `json:"username"`
	FileCount         int    `json:"fileCount"`
	Files             []File `json:"files"`
	LockedFileCount   int    `json:"lockedFileCount"`
	LockedFiles       []File `json:"lockedFiles"`
	HasFreeUploadSlot bool   `json:"hasFreeUploadSlot"`
	QueueLength       int64  `json:"queueLength"`
	UploadSpeed       int64  `json:"uploadSpeed"`
}

// Search is the search status object returned by POST/GET /searches.
type Search struct {
	ID            string `json:"id"`
	SearchText    string `json:"searchText"`
	State         string `json:"state"`
	IsComplete    bool   `json:"isComplete"`
	FileCount     int    `json:"fileCount"`
	ResponseCount int    `json:"responseCount"`
}

// SearchOptions are per-search knobs, normally taken from the speed preset.
type SearchOptions struct {
	TimeoutSeconds           int
	ResponseLimit            int
	FileLimit                int
	MinimumResponseFileCount int
	MinimumPeerUploadSpeed   int
	MaximumPeerQueueLength   int
}

// StateFlags is the raw transfer state. slskd serializes the Soulseek.NET
// [Flags] enum TransferStates as a comma-separated string such as
// "Completed, Succeeded" or "Queued, Remotely" (JsonStringEnumConverter).
// A numeric form is also accepted defensively in case a deployment
// serializes enums as integers.
type StateFlags string

// UnmarshalJSON accepts either a string or a number.
func (s *StateFlags) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = StateFlags(str)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*s = StateFlags(flagsFromInt(n))
	return nil
}

// Soulseek.NET TransferStates bit values.
var transferStateBits = []struct {
	name string
	bit  int
}{
	{"Requested", 1}, {"Queued", 2}, {"Initializing", 4}, {"InProgress", 8},
	{"Completed", 16}, {"Succeeded", 32}, {"Cancelled", 64}, {"TimedOut", 128},
	{"Errored", 256}, {"Rejected", 512}, {"Aborted", 1024}, {"Locally", 2048}, {"Remotely", 4096},
}

func flagsFromInt(n int) string {
	if n == 0 {
		return "None"
	}
	var parts []string
	for _, f := range transferStateBits {
		if n&f.bit != 0 {
			parts = append(parts, f.name)
		}
	}
	return strings.Join(parts, ", ")
}

func (s StateFlags) has(name string) bool {
	for _, p := range strings.Split(string(s), ",") {
		if strings.EqualFold(strings.TrimSpace(p), name) {
			return true
		}
	}
	return false
}

// State is slskify's clean view of a transfer state.
type State int

const (
	StateQueued State = iota
	StateInitializing
	StateInProgress
	StateSucceeded
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateQueued:
		return "Queued"
	case StateInitializing:
		return "Initializing"
	case StateInProgress:
		return "InProgress"
	case StateSucceeded:
		return "Succeeded"
	case StateFailed:
		return "Failed"
	}
	return "State(" + strconv.Itoa(int(s)) + ")"
}

// Terminal reports whether the state is final.
func (s State) Terminal() bool { return s == StateSucceeded || s == StateFailed }

// MapState converts raw flags to the internal enum plus, for failures, the
// specific reason (rejected, errored, timed out, cancelled, aborted).
func MapState(f StateFlags) (State, string) {
	if f.has("Completed") {
		if f.has("Succeeded") {
			return StateSucceeded, ""
		}
		for _, r := range []struct{ flag, reason string }{
			{"Rejected", "rejected"}, {"Errored", "errored"}, {"TimedOut", "timed out"},
			{"Cancelled", "cancelled"}, {"Aborted", "aborted"},
		} {
			if f.has(r.flag) {
				return StateFailed, r.reason
			}
		}
		return StateFailed, "failed"
	}
	switch {
	case f.has("InProgress"):
		return StateInProgress, ""
	case f.has("Initializing"):
		return StateInitializing, ""
	default: // None, Requested, Queued (Locally/Remotely)
		return StateQueued, ""
	}
}

// Transfer is a download as reported by GET /transfers/downloads.
type Transfer struct {
	ID               string     `json:"id"`
	Username         string     `json:"username"`
	Filename         string     `json:"filename"`
	Size             int64      `json:"size"`
	State            StateFlags `json:"state"`
	StateDescription string     `json:"stateDescription"`
	BytesTransferred int64      `json:"bytesTransferred"`
	AverageSpeed     float64    `json:"averageSpeed"`
	PercentComplete  float64    `json:"percentComplete"`
	// PlaceInQueue is reported by slskd when known; queue-position-aware
	// waiting is a planned improvement and does not use this yet.
	PlaceInQueue *int   `json:"placeInQueue"`
	Exception    string `json:"exception"`
	Removed      bool   `json:"removed"`
}

// Status maps the raw state.
func (t Transfer) Status() (State, string) { return MapState(t.State) }

type transferUser struct {
	Username    string `json:"username"`
	Directories []struct {
		Directory string     `json:"directory"`
		Files     []Transfer `json:"files"`
	} `json:"directories"`
}
