// Package fakeapi is an in-memory fake of the Spotify Web API and the
// slskd API, used by command tests and for exercising the TUI locally
// without real services. It implements only what slskify calls.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unicode"
)

// LibFile is a file some peer shares.
type LibFile struct {
	User    string
	Path    string
	SizeMB  float64
	BitRate int
	Length  int // seconds; 0 = unknown
	// Behavior: "" succeeds, "reject" is rejected remotely, "queue" stays
	// queued forever.
	Behavior string
	Locked   bool
	FreeSlot bool
	Speed    int64
}

// Track is a Spotify track served by the fake.
type Track struct {
	ID, Name, Album, ISRC string
	Artists               []string
	DurationS, Number     int
	Year                  int
	Explicit              bool
}

// Server is the fake. Use Handler with httptest or a real listener.
type Server struct {
	PlaylistID   string
	PlaylistName string
	Tracks       []Track
	// LocalFiles is the number of local-file items appended to the playlist
	// (reported as skipped).
	LocalFiles int
	Library    []LibFile
	// TransferSteps is how many Downloads() calls a transfer takes to finish.
	TransferSteps int

	mu        sync.Mutex
	searches  map[string]string // id → text
	transfers []*transfer
	nextID    int
	// SearchCount counts POST /searches (for tests).
	SearchCount int
}

type transfer struct {
	id, user, file string
	size           int64
	behavior       string
	step           int
	removed        bool
}

// Handler serves both APIs: Spotify under /v1 and /api/token, slskd under
// /api/v0.
func (s *Server) Handler() http.Handler {
	if s.TransferSteps == 0 {
		s.TransferSteps = 4
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"access_token": "fake", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/v1/", s.spotify)
	mux.HandleFunc("/api/v0/", s.slskd)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) apiTrack(t Track) map[string]any {
	var as []map[string]string
	for _, a := range t.Artists {
		as = append(as, map[string]string{"name": a})
	}
	return map[string]any{
		"id": t.ID, "name": t.Name, "type": "track", "uri": "spotify:track:" + t.ID, "artists": as,
		"album":        map[string]any{"id": "alb" + t.ID, "name": t.Album, "album_type": "album", "release_date": fmt.Sprintf("%d-01-01", t.Year), "artists": as[:1]},
		"track_number": t.Number, "disc_number": 1, "duration_ms": t.DurationS * 1000, "explicit": t.Explicit,
		"external_ids": map[string]string{"isrc": t.ISRC},
	}
}

func (s *Server) spotify(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case p == "/playlists/"+s.PlaylistID:
		writeJSON(w, map[string]any{"id": s.PlaylistID, "name": s.PlaylistName, "owner": map[string]string{"id": "me", "display_name": "Fake User"}})
	case p == "/playlists/"+s.PlaylistID+"/items":
		var items []map[string]any
		for _, t := range s.Tracks {
			items = append(items, map[string]any{"is_local": false, "item": s.apiTrack(t)})
		}
		for i := 0; i < s.LocalFiles; i++ {
			items = append(items, map[string]any{"is_local": true, "item": map[string]any{"name": fmt.Sprintf("local %d", i+1), "uri": "spotify:local:x", "type": "track"}})
		}
		writeJSON(w, map[string]any{"items": items, "next": nil, "total": len(items), "offset": 0, "limit": 50})
	case strings.HasPrefix(p, "/tracks/"):
		id := strings.TrimPrefix(p, "/tracks/")
		for _, t := range s.Tracks {
			if t.ID == id {
				writeJSON(w, s.apiTrack(t))
				return
			}
		}
		if len(s.Tracks) > 0 { // Ping uses a fixed well-known ID
			writeJSON(w, s.apiTrack(s.Tracks[0]))
			return
		}
		http.NotFound(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": map[string]any{"status": 404, "message": "Resource not found"}})
	}
}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func (s *Server) results(text string) []map[string]any {
	q := tokens(text)
	byUser := map[string][]map[string]any{}
	var users []string
	for _, f := range s.Library {
		pt := " " + strings.Join(tokens(f.Path), " ") + " "
		ok := len(q) > 0
		for _, t := range q {
			if !strings.Contains(pt, " "+t+" ") {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		ext := f.Path[strings.LastIndex(f.Path, ".")+1:]
		file := map[string]any{"filename": f.Path, "size": int64(f.SizeMB * (1 << 20)), "extension": ext, "isLocked": f.Locked}
		if f.BitRate > 0 {
			file["bitRate"] = f.BitRate
		}
		if f.Length > 0 {
			file["length"] = f.Length
		}
		if _, seen := byUser[f.User]; !seen {
			users = append(users, f.User)
		}
		byUser[f.User] = append(byUser[f.User], file)
	}
	out := []map[string]any{}
	for _, u := range users {
		var free bool
		var speed int64
		for _, f := range s.Library {
			if f.User == u {
				free, speed = f.FreeSlot, f.Speed
			}
		}
		out = append(out, map[string]any{"username": u, "files": byUser[u], "fileCount": len(byUser[u]), "hasFreeUploadSlot": free, "uploadSpeed": speed, "queueLength": 0})
	}
	return out
}

func (s *Server) slskd(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v0")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searches == nil {
		s.searches = map[string]string{}
	}
	switch {
	case p == "/application":
		writeJSON(w, map[string]any{"version": "fake"})
	case p == "/searches" && r.Method == http.MethodPost:
		var req struct {
			ID         string `json:"id"`
			SearchText string `json:"searchText"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.searches[req.ID] = req.SearchText
		s.SearchCount++
		writeJSON(w, map[string]any{"id": req.ID, "searchText": req.SearchText, "isComplete": true, "state": "Completed"})
	case strings.HasPrefix(p, "/searches/"):
		id, sub, _ := strings.Cut(strings.TrimPrefix(p, "/searches/"), "/")
		switch {
		case r.Method == http.MethodDelete:
			delete(s.searches, id)
			w.WriteHeader(http.StatusNoContent)
		case sub == "responses":
			writeJSON(w, s.results(s.searches[id]))
		default:
			writeJSON(w, map[string]any{"id": id, "isComplete": true, "state": "Completed"})
		}
	case p == "/transfers/downloads" && r.Method == http.MethodGet:
		s.downloads(w)
	case strings.HasPrefix(p, "/transfers/downloads/") && r.Method == http.MethodPost:
		user := strings.TrimPrefix(p, "/transfers/downloads/")
		var files []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&files)
		var enq []map[string]any
		for _, f := range files {
			s.nextID++
			t := &transfer{id: fmt.Sprintf("t%d", s.nextID), user: user, file: f.Filename, size: f.Size}
			for _, lf := range s.Library {
				if lf.User == user && lf.Path == f.Filename {
					t.behavior = lf.Behavior
				}
			}
			s.transfers = append(s.transfers, t)
			enq = append(enq, map[string]any{"id": t.id, "username": user, "filename": f.Filename, "size": f.Size, "state": "Queued, Locally"})
		}
		writeJSON(w, map[string]any{"enqueued": enq, "failed": []any{}})
	case strings.HasPrefix(p, "/transfers/downloads/") && r.Method == http.MethodDelete:
		parts := strings.Split(strings.TrimPrefix(p, "/transfers/downloads/"), "/")
		for _, t := range s.transfers {
			if len(parts) == 2 && t.id == parts[1] {
				if t.behavior != "reject" {
					t.behavior = "cancelled"
				}
				if r.URL.Query().Get("remove") == "true" {
					t.removed = true
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// Transfers returns (user, filename, state) of every transfer ever
// enqueued, for assertions.
func (s *Server) Transfers() [][3]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][3]string
	for _, t := range s.transfers {
		out = append(out, [3]string{t.user, t.file, t.behavior})
	}
	return out
}

func (s *Server) downloads(w http.ResponseWriter) {
	type dir struct {
		Directory string           `json:"directory"`
		Files     []map[string]any `json:"files"`
	}
	type user struct {
		Username    string `json:"username"`
		Directories []dir  `json:"directories"`
	}
	out := []user{}
	idx := map[string]int{}
	for _, t := range s.transfers {
		if t.removed {
			continue
		}
		t.step++
		state, pct := "Queued, Remotely", 0.0
		switch t.behavior {
		case "reject":
			state = "Completed, Rejected"
		case "cancelled":
			state = "Completed, Cancelled"
		case "queue":
		default:
			switch {
			case t.step >= s.TransferSteps:
				state, pct = "Completed, Succeeded", 100
			case t.step > 1:
				state, pct = "InProgress", float64(t.step-1)*100/float64(s.TransferSteps-1)
			}
		}
		i, ok := idx[t.user]
		if !ok {
			i = len(out)
			idx[t.user] = i
			out = append(out, user{Username: t.user, Directories: []dir{{Directory: "x"}}})
		}
		out[i].Directories[0].Files = append(out[i].Directories[0].Files, map[string]any{
			"id": t.id, "username": t.user, "filename": t.file, "size": t.size, "state": state,
			"bytesTransferred": int64(pct / 100 * float64(t.size)), "averageSpeed": 512000.0, "percentComplete": pct,
		})
	}
	writeJSON(w, out)
}

// Demo returns a small scenario: an auto match whose first peer rejects
// (fallback), wrong-version traps, an unknown-duration track (review), a
// not-found track and a local file (skipped).
func Demo() *Server {
	return &Server{
		PlaylistID: "3cEYpjA9oz9GiPac4AsH4n", PlaylistName: "Fake Mix", LocalFiles: 1,
		Tracks: []Track{
			{ID: "t1", Name: "Bohemian Rhapsody", Artists: []string{"Queen"}, Album: "A Night at the Opera", DurationS: 354, Number: 11, Year: 1975, ISRC: "GBUM71029604"},
			{ID: "t2", Name: "Get Lucky (feat. Pharrell Williams & Nile Rodgers)", Artists: []string{"Daft Punk", "Pharrell Williams", "Nile Rodgers"}, Album: "Random Access Memories", DurationS: 369, Number: 8, Year: 2013},
			{ID: "t3", Name: "Hyperballad", Artists: []string{"Björk"}, Album: "Post", DurationS: 321, Number: 3, Year: 1995},
			{ID: "t4", Name: "Nonexistent Song Xyzzy", Artists: []string{"Nobody Atall"}, Album: "Void", DurationS: 200, Number: 1, Year: 2020},
		},
		Library: []LibFile{
			{User: "rejector", Path: `@@music\Queen\A Night at the Opera (1975)\11 - Bohemian Rhapsody.flac`, SizeMB: 40, Length: 355, Behavior: "reject", FreeSlot: true, Speed: 900000},
			{User: "goodpeer", Path: `Music\Queen\1975 - A Night at the Opera\11. Queen - Bohemian Rhapsody.flac`, SizeMB: 38, Length: 354, FreeSlot: true, Speed: 500000},
			{User: "karaoke", Path: `Karaoke\Queen - Bohemian Rhapsody (Karaoke Version).flac`, SizeMB: 41, Length: 356, FreeSlot: true, Speed: 900000},
			{User: "liveguy", Path: `Queen\Live at Wembley\Bohemian Rhapsody (Live).mp3`, SizeMB: 9, BitRate: 320, Length: 361, FreeSlot: true},
			{User: "dp", Path: `Daft Punk\Random Access Memories\08 - Get Lucky.mp3`, SizeMB: 14, BitRate: 320, Length: 369, FreeSlot: true, Speed: 300000},
			{User: "dp2", Path: `Daft Punk - Get Lucky (Radio Edit).flac`, SizeMB: 25, Length: 248, FreeSlot: true, Speed: 900000},
			{User: "nolen", Path: `Bjork\Post\03 Hyperballad.flac`, SizeMB: 33, FreeSlot: true},
			{User: "nolen2", Path: `Björk - Post\03. Björk - Hyperballad.mp3`, SizeMB: 7.5, BitRate: 192, FreeSlot: true},
		},
	}
}
