package qb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFilesParsesQBittorrentListing checks the /api/v2/torrents/files client
// used by the M6.1c multi-file import.
func TestFilesParsesQBittorrentListing(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathLogin:
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid-test", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case pathFiles:
			if got := r.URL.Query().Get("hash"); got != hash {
				t.Errorf("hash query = %q", got)
			}
			_ = json.NewEncoder(w).Encode([]TorrentFile{
				{Name: "dir/a.bin", Size: 3, Priority: 1},
				{Name: "dir/b.bin", Size: 4, Priority: 0},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	files, err := c.Files(context.Background(), hash)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	if files[0].Name != "dir/a.bin" || files[0].Size != 3 || files[0].Priority != 1 {
		t.Fatalf("first file = %+v", files[0])
	}
	if files[1].Priority != 0 {
		t.Fatalf("skipped file = %+v", files[1])
	}
}
