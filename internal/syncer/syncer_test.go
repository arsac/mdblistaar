package syncer_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golift.io/starr"
	"golift.io/starr/radarr"
	"golift.io/starr/sonarr"

	"github.com/arsac/mdblistaar/internal/config"
	"github.com/arsac/mdblistaar/internal/mdblist"
	"github.com/arsac/mdblistaar/internal/syncer"
)

// fakeArr serves the slice of the Radarr or Sonarr v3 API a Sync uses and records
// every add and tag it is asked to create.
type fakeArr struct {
	mu      sync.Mutex
	idField string
	added   []map[string]any
	newTags []string
}

type arrSpec struct {
	itemPath, exclusionPath, idField string
	library, excluded                []int64
	failAddID                        int64
	down                             bool
}

func newFakeArr(t *testing.T, spec arrSpec) (*fakeArr, *httptest.Server) {
	t.Helper()

	f := &fakeArr{idField: spec.idField}
	mux := http.NewServeMux()

	ids := func(field string, ids []int64) []map[string]any {
		out := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]any{field: id, "title": "existing"})
		}

		return out
	}

	mux.HandleFunc("GET /api/v3/qualityProfile", func(w http.ResponseWriter, _ *http.Request) {
		if spec.down {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}

		writeJSON(w, []map[string]any{{"id": 7, "name": "HD"}, {"id": 8, "name": "Anime"}})
	})
	mux.HandleFunc("GET /api/v3/rootFolder", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"id": 1, "path": "/media/main"}, {"id": 2, "path": "/media/anime"}})
	})
	mux.HandleFunc("GET /api/v3/tag", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"id": 1, "label": "mdblist"}})
	})
	mux.HandleFunc("POST /api/v3/tag", func(w http.ResponseWriter, r *http.Request) {
		var tag starr.Tag
		_ = json.NewDecoder(r.Body).Decode(&tag)

		f.mu.Lock()
		f.newTags = append(f.newTags, tag.Label)
		tag.ID = 100 + len(f.newTags)
		f.mu.Unlock()

		writeJSON(w, tag)
	})
	mux.HandleFunc("GET /api/v3/"+spec.itemPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, ids(spec.idField, spec.library))
	})
	mux.HandleFunc("GET /api/v3/"+spec.exclusionPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, ids(spec.idField, spec.excluded))
	})
	mux.HandleFunc("GET /api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("tmdbId"), 10, 64)
		writeJSON(w, map[string]any{"tmdbId": id, "title": fmt.Sprint("movie ", id), "titleSlug": "slug", "year": 2024})
	})
	mux.HandleFunc("GET /api/v3/series/lookup", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Query().Get("term"), "tvdbid:"), 10, 64)
		writeJSON(w, []map[string]any{{"tvdbId": id, "title": fmt.Sprint("show ", id), "titleSlug": "slug"}})
	})
	mux.HandleFunc("POST /api/v3/"+spec.itemPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		if id, _ := body[spec.idField].(float64); int64(id) == spec.failAddID {
			http.Error(w, `[{"errorMessage": "already exists"}]`, http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		f.added = append(f.added, body)
		f.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		writeJSON(w, body)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return f, srv
}

func (f *fakeArr) addedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]int64, 0, len(f.added))
	for _, a := range f.added {
		id, _ := a[f.idField].(float64)
		out = append(out, int64(id))
	}

	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newMDBList serves each list id's items; a list id with no entry returns 500.
func newMDBList(t *testing.T, lists map[int64]mdblist.Items) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /lists/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)

		items, ok := lists[id]
		if !ok {
			http.Error(w, "upstream down", http.StatusInternalServerError)
			return
		}

		writeJSON(w, items)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

type env struct {
	radarr, sonarr *fakeArr
	syncer         *syncer.Syncer
}

type setup struct {
	lists          map[int64]mdblist.Items
	radarr, sonarr arrSpec
	dryRun         bool
}

func newEnv(t *testing.T, s setup) env {
	t.Helper()

	s.radarr.itemPath, s.radarr.exclusionPath, s.radarr.idField = "movie", "exclusions", "tmdbId"
	s.sonarr.itemPath, s.sonarr.exclusionPath, s.sonarr.idField = "series", "importlistexclusion", "tvdbId"

	r, rSrv := newFakeArr(t, s.radarr)
	so, sSrv := newFakeArr(t, s.sonarr)
	mdb := newMDBList(t, s.lists)

	return env{
		radarr: r,
		sonarr: so,
		syncer: syncer.New(
			slog.New(slog.DiscardHandler),
			mdblist.New(mdb.Client(), mdb.URL, "key", "mdblistaar/test"),
			radarr.New(starr.New("key", rSrv.URL, 0)),
			sonarr.New(starr.New("key", sSrv.URL, 0)),
			s.dryRun,
		),
	}
}

func sourceList(name string, id int64, root string) config.SourceList {
	return config.SourceList{
		Name: name,
		ID:   id,
		Radarr: config.RadarrOptions{
			RootFolderPath:      root,
			QualityProfile:      "HD",
			Tags:                []string{"mdblist", "New-Tag"},
			Monitor:             "movieOnly",
			SearchForMovie:      new(true),
			MinimumAvailability: "released",
		},
		Sonarr: config.SonarrOptions{
			RootFolderPath:           root,
			QualityProfile:           "Anime",
			Monitor:                  "future",
			SearchForMissingEpisodes: new(false),
			SeriesType:               "anime",
			SeasonFolder:             new(true),
		},
	}
}

func equalIDs(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestRun_AddsOnlyMissingTitles(t *testing.T) {
	t.Parallel()

	e := newEnv(t, setup{
		lists: map[int64]mdblist.Items{1: {
			Movies: []mdblist.Item{{TMDBID: 10}, {TMDBID: 11}, {TMDBID: 12}},
			Shows:  []mdblist.Item{{TMDBID: 900, TVDBID: 20}, {TMDBID: 901, TVDBID: 21}, {TMDBID: 902}},
		}},
		radarr: arrSpec{library: []int64{11}, excluded: []int64{12}},
		sonarr: arrSpec{excluded: []int64{21}},
	})

	if err := e.syncer.Run(t.Context(), []config.SourceList{sourceList("a", 1, "/media/anime/")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	equalIDs(t, "radarr adds", e.radarr.addedIDs(), 10)
	equalIDs(t, "sonarr adds", e.sonarr.addedIDs(), 20)

	movie := e.radarr.added[0]
	if movie["rootFolderPath"] != "/media/anime" || movie["qualityProfileId"] != 7.0 ||
		movie["title"] != "movie 10" || movie["minimumAvailability"] != "released" ||
		movie["monitored"] != true || fmt.Sprint(movie["tags"]) != "[1 101]" ||
		fmt.Sprint(movie["addOptions"]) != "map[monitor:movieOnly searchForMovie:true]" {
		t.Errorf("radarr add = %v", movie)
	}

	show := e.sonarr.added[0]
	if show["rootFolderPath"] != "/media/anime" || show["qualityProfileId"] != 8.0 ||
		show["seriesType"] != "anime" || show["seasonFolder"] != true || show["title"] != "show 20" ||
		fmt.Sprint(show["addOptions"]) != "map[monitor:future searchForMissingEpisodes:false]" {
		t.Errorf("sonarr add = %v", show)
	}

	if !slices.Equal(e.radarr.newTags, []string{"New-Tag"}) {
		t.Errorf("radarr created tags %v, want [New-Tag]", e.radarr.newTags)
	}
}

func TestRun_FirstSourceListWins(t *testing.T) {
	t.Parallel()

	shared := mdblist.Items{Movies: []mdblist.Item{{TMDBID: 10}}}
	e := newEnv(t, setup{lists: map[int64]mdblist.Items{1: shared, 2: shared}})

	err := e.syncer.Run(t.Context(), []config.SourceList{
		sourceList("first", 1, "/media/anime"),
		sourceList("second", 2, "/media/main"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	equalIDs(t, "radarr adds", e.radarr.addedIDs(), 10)

	if root := e.radarr.added[0]["rootFolderPath"]; root != "/media/anime" {
		t.Errorf("rootFolderPath = %v, want the first list's /media/anime", root)
	}
}

func TestRun_DryRunWritesNothing(t *testing.T) {
	t.Parallel()

	e := newEnv(t, setup{
		lists: map[int64]mdblist.Items{1: {
			Movies: []mdblist.Item{{TMDBID: 10}},
			Shows:  []mdblist.Item{{TVDBID: 20}},
		}},
		dryRun: true,
	})

	if err := e.syncer.Run(t.Context(), []config.SourceList{sourceList("a", 1, "/media/main")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(e.radarr.added)+len(e.sonarr.added)+len(e.radarr.newTags) != 0 {
		t.Errorf("dry run wrote: radarr adds %v, sonarr adds %v, tags %v",
			e.radarr.added, e.sonarr.added, e.radarr.newTags)
	}
}

func TestRun_NoArrsSkipsMDBList(t *testing.T) {
	t.Parallel()

	// List 1 isn't served, so fetching it would add a "source list" error.
	e := newEnv(t, setup{radarr: arrSpec{down: true}, sonarr: arrSpec{down: true}})

	err := e.syncer.Run(t.Context(), []config.SourceList{sourceList("a", 1, "/media/main")})
	if err == nil || strings.Contains(err.Error(), "source list") {
		t.Fatalf("err = %v, want only the Arr failures", err)
	}
}

func TestRun_FailuresAreIsolated(t *testing.T) {
	t.Parallel()

	items := mdblist.Items{
		Movies: []mdblist.Item{{TMDBID: 10, Title: "Bad"}, {TMDBID: 11}},
		Shows:  []mdblist.Item{{TVDBID: 20}},
	}

	tests := []struct {
		name       string
		setup      setup
		lists      []config.SourceList
		wantErr    string
		wantMovies []int64
		wantShows  []int64
	}{
		{
			name:       "one add fails",
			setup:      setup{radarr: arrSpec{failAddID: 10}},
			wantErr:    `radarr: add "Bad" (10)`,
			wantMovies: []int64{11},
			wantShows:  []int64{20},
		},
		{
			name:      "radarr down",
			setup:     setup{radarr: arrSpec{down: true}},
			wantErr:   "radarr: quality profiles",
			wantShows: []int64{20},
		},
		{
			name: "unknown quality profile",
			lists: func() []config.SourceList {
				l := sourceList("a", 1, "/media/main")
				l.Sonarr.QualityProfile = "Nope"

				return []config.SourceList{l}
			}(),
			wantErr:    `sonarr: quality profile "Nope" not found`,
			wantMovies: []int64{10, 11},
		},
		{
			name: "unknown root folder",
			lists: func() []config.SourceList {
				l := sourceList("a", 1, "/media/main")
				l.Radarr.RootFolderPath = "/elsewhere"

				return []config.SourceList{l}
			}(),
			wantErr:   `radarr: root folder "/elsewhere" not found`,
			wantShows: []int64{20},
		},
		{
			name:       "one source list unavailable",
			lists:      []config.SourceList{sourceList("gone", 99, "/media/main"), sourceList("a", 1, "/media/main")},
			wantErr:    `source list "gone"`,
			wantMovies: []int64{10, 11},
			wantShows:  []int64{20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.setup.lists = map[int64]mdblist.Items{1: items}
			if tt.lists == nil {
				tt.lists = []config.SourceList{sourceList("a", 1, "/media/main")}
			}

			e := newEnv(t, tt.setup)

			err := e.syncer.Run(t.Context(), tt.lists)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}

			equalIDs(t, "radarr adds", e.radarr.addedIDs(), tt.wantMovies...)
			equalIDs(t, "sonarr adds", e.sonarr.addedIDs(), tt.wantShows...)
		})
	}
}
