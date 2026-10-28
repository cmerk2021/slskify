package spotify

import (
	"errors"
	"fmt"
)

// ErrKind classifies Spotify errors so callers (and the UI) can react
// without inspecting HTTP status codes.
type ErrKind int

const (
	ErrKindOther ErrKind = iota
	ErrKindAuth
	ErrKindNotFound
	ErrKindForbidden
	ErrKindPremiumRequired
	ErrKindRateLimited
	ErrKindNetwork
)

// Error is a human-readable Spotify error. Message explains what happened,
// Hint suggests a workaround. Status is kept for logging/tests only and is
// never the whole story shown to the user.
type Error struct {
	Kind    ErrKind
	Status  int
	Message string
	Hint    string
	Cause   error
}

func (e *Error) Error() string {
	s := e.Message
	if e.Hint != "" {
		s += " — " + e.Hint
	}
	return s
}

func (e *Error) Unwrap() error { return e.Cause }

// IsKind reports whether err is a *Error of the given kind.
func IsKind(err error, k ErrKind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

const playlistWorkaround = "make the playlist public, or copy its tracks into a new public playlist you own and paste that link"

// mapPlaylistError turns a raw API error for a playlist request into a
// specific explanation. Newer Spotify apps cannot read Spotify-owned
// editorial/algorithmic playlists, and private playlists need user auth
// (not supported in v1); both surface as 401/403/404.
func mapPlaylistError(err error, id string) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Kind {
	case ErrKindNotFound, ErrKindForbidden:
		if IsEditorialPlaylistID(id) {
			return &Error{Kind: e.Kind, Status: e.Status,
				Message: "this looks like a Spotify-owned editorial or personalised playlist, which Spotify no longer lets third-party apps read",
				Hint:    "copy its tracks into a new public playlist you own (select all → Add to playlist) and paste that link"}
		}
		return &Error{Kind: e.Kind, Status: e.Status,
			Message: "Spotify would not return this playlist: it is private, deleted, or not readable by this app",
			Hint:    playlistWorkaround}
	}
	return err
}

func mapEntityError(err error, kind Kind) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Kind == ErrKindNotFound {
		return &Error{Kind: e.Kind, Status: e.Status,
			Message: fmt.Sprintf("Spotify could not find this %s", kind),
			Hint:    "check that the link is correct and the item is available"}
	}
	return err
}
