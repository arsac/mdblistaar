package mdblist_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/arsac/mdblistaar/internal/mdblist"
)

const apiKey = "secret-key"

func TestListItems_Paginates(t *testing.T) {
	t.Parallel()

	var offsets []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lists/42/items" || r.URL.Query().Get("apikey") != apiKey ||
			r.UserAgent() != "mdblistaar/test" {
			http.Error(w, "bad request "+r.URL.String(), http.StatusBadRequest)
			return
		}

		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)

		if offset == "0" {
			w.Header().Set("X-Has-More", "true")
			fmt.Fprint(
				w,
				`{"movies": [{"id": 1, "title": "Movie", "tvdb_id": null, "release_year": 2024}], "shows": []}`,
			)

			return
		}

		w.Header().Set("X-Has-More", "false")
		fmt.Fprint(w, `{"movies": [], "shows": [{"id": 2, "title": "Show", "tvdb_id": 3, "release_year": 2020}]}`)
	}))
	t.Cleanup(srv.Close)

	items, err := mdblist.New(srv.Client(), srv.URL, apiKey, "mdblistaar/test").ListItems(t.Context(), 42)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}

	// The first page held one item, so the second starts at offset 1.
	if got, want := strings.Join(offsets, ","), "0,1"; got != want {
		t.Errorf("offsets = %s, want %s", got, want)
	}

	wantMovie := mdblist.Item{TMDBID: 1, Title: "Movie", Year: 2024}
	if len(items.Movies) != 1 || items.Movies[0] != wantMovie {
		t.Errorf("Movies = %+v, want [%+v]", items.Movies, wantMovie)
	}

	wantShow := mdblist.Item{TMDBID: 2, TVDBID: 3, Title: "Show", Year: 2020}
	if len(items.Shows) != 1 || items.Shows[0] != wantShow {
		t.Errorf("Shows = %+v, want [%+v]", items.Shows, wantShow)
	}
}

func TestListItems_StopsOnEmptyPage(t *testing.T) {
	t.Parallel()

	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.Header().Set("X-Has-More", "true")
		fmt.Fprint(w, `{"movies": [], "shows": []}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := mdblist.New(srv.Client(), srv.URL, apiKey, "mdblistaar/test").ListItems(t.Context(), 1); err != nil {
		t.Fatalf("ListItems: %v", err)
	}

	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestListItems_Errors(t *testing.T) {
	t.Parallel()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error": "list not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(failing.Close)

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	tests := []struct {
		name    string
		srv     *httptest.Server
		wantErr string
	}{
		{name: "http status", srv: failing, wantErr: "HTTP " + strconv.Itoa(http.StatusNotFound)},
		{name: "unreachable", srv: closed, wantErr: "list 7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := mdblist.New(http.DefaultClient, tt.srv.URL, apiKey, "mdblistaar/test").ListItems(t.Context(), 7)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}

			if strings.Contains(err.Error(), apiKey) {
				t.Errorf("error leaks the API key: %v", err)
			}
		})
	}
}
