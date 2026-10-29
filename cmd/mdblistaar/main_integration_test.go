//go:build integration

package main_test

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golift.io/starr"
	"golift.io/starr/radarr"
	"golift.io/starr/sonarr"

	"github.com/arsac/mdblistaar/internal/mdblist"
)

// arrKey matches compose.yaml.
const arrKey = "integration-test-key"

// "Star Wars: The Clone Wars" is on the list twice: as a 2008 film and as a 2008 series.
// Blocking the series in Sonarr must not stop the film reaching Radarr.
const (
	blockedShowTVDB  = 83268  // Star Wars: The Clone Wars (series)
	cloneWarsFilm    = 12180  // Star Wars: The Clone Wars (film)
	blockedMovieTMDB = 181812 // Star Wars: The Rise of Skywalker
)

// TestIntegration_SyncTwice runs the real binary against a live mixed mdblist list and the
// Sonarr and Radarr from compose.yaml, twice. The first run must add every listed title
// with its Add Options, skipping titles blocked in the Arrs; the second must find nothing
// left to do.
func TestIntegration_SyncTwice(t *testing.T) {
	t.Parallel()

	mdbKey := os.Getenv("MDBLIST_API_KEY")
	if mdbKey == "" {
		t.Fatal("MDBLIST_API_KEY is not set: put it in .env, which mise loads")
	}

	radarrURL := cmp.Or(os.Getenv("RADARR_URL"), "http://localhost:7878")
	sonarrURL := cmp.Or(os.Getenv("SONARR_URL"), "http://localhost:8989")
	r := radarr.New(starr.New(arrKey, radarrURL, 0))
	s := sonarr.New(starr.New(arrKey, sonarrURL, 0))

	waitReady(t, "radarr", func(ctx context.Context) error { _, err := r.GetSystemStatusContext(ctx); return err })
	waitReady(t, "sonarr", func(ctx context.Context) error { _, err := s.GetSystemStatusContext(ctx); return err })

	lists := mdblist.New(&http.Client{Timeout: 30 * time.Second}, mdblist.DefaultBaseURL, mdbKey, "mdblistaar/test")

	listID := fixtureListID(t, mdbKey)

	items, err := lists.ListItems(t.Context(), listID)
	must(t, err)

	prepare(t, r, s, items)

	bin := build(t)
	cfgPath := writeConfig(t, radarrURL, sonarrURL, listID)

	for i, want := range [][]synced{
		{{List: "fixture", Arr: "radarr", Added: 3, Excluded: 1}, {List: "fixture", Arr: "sonarr", Added: 3, Excluded: 1}},
		{
			{List: "fixture", Arr: "radarr", Existing: 3, Excluded: 1},
			{List: "fixture", Arr: "sonarr", Existing: 3, Excluded: 1},
		},
	} {
		if got := runSync(t, bin, cfgPath, mdbKey); !slices.Equal(got, want) {
			t.Errorf("run %d synced %+v, want %+v", i+1, got, want)
		}

		// After either run the libraries hold exactly the listed titles that aren't blocked.
		assertMovies(t, r, items.Movies, blockedMovieTMDB)
		assertShows(t, s, items.Shows, blockedShowTVDB)
	}
}

// synced is the per-Source-List, per-Arr summary line a Sync logs.
type synced struct {
	List     string `json:"list"`
	Arr      string `json:"arr"`
	Added    int    `json:"added"`
	Existing int    `json:"existing"`
	Excluded int    `json:"excluded"`
	NoID     int    `json:"no_id"`
	Failed   int    `json:"failed"`
}

func runSync(t *testing.T, bin, cfgPath, mdbKey string) []synced {
	t.Helper()

	var stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), bin)
	cmd.Stderr = &stderr

	// Only what the binary reads, so nothing from the host environment leaks in.
	for name, value := range map[string]string{
		"CONFIG_PATH":     cfgPath,
		"MDBLIST_API_KEY": mdbKey,
		"RADARR_API_KEY":  arrKey,
		"SONARR_API_KEY":  arrKey,
	} {
		cmd.Env = append(cmd.Env, name+"="+value)
	}

	if err := cmd.Run(); err != nil {
		t.Fatalf("mdblistaar: %v\n%s", err, stderr.String())
	}

	var lines []synced

	scanner := bufio.NewScanner(&stderr)
	for scanner.Scan() {
		var entry struct {
			synced

			Msg string `json:"msg"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Msg == "synced" {
			lines = append(lines, entry.synced)
		}
	}

	// One Source List, synced into both Arrs.
	if len(lines) != 2 {
		t.Fatalf("got %d synced lines, want 2:\n%s", len(lines), stderr.String())
	}

	return lines
}

func build(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "mdblistaar")

	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	return bin
}

func writeConfig(t *testing.T, radarrURL, sonarrURL string, listID int64) string {
	t.Helper()

	cfg := fmt.Sprintf(`radarr:
  url: %s
sonarr:
  url: %s
sourceLists:
  - name: fixture
    id: %d
    radarr:
      rootFolderPath: /movies
      qualityProfile: Any
      tags: [mdblistaar-it]
      monitor: movieOnly
      searchForMovie: false
      minimumAvailability: released
    sonarr:
      rootFolderPath: /tv
      qualityProfile: Any
      tags: [mdblistaar-it]
      monitor: future
      searchForMissingEpisodes: false
      seriesType: standard
      seasonFolder: true
`, radarrURL, sonarrURL, listID)

	path := filepath.Join(t.TempDir(), "config.yaml")
	must(t, os.WriteFile(path, []byte(cfg), 0o600))

	return path
}

func waitReady(t *testing.T, name string, status func(context.Context) error) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Minute)

	for {
		err := status(t.Context())
		if err == nil {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("%s not ready (is `docker compose up -d` running?): %v", name, err)
		}

		time.Sleep(2 * time.Second)
	}
}

// fixtureListID returns the id of the only list on the mdblist account the integration
// API key belongs to. That account exists only for these tests, and its one list is a
// private static list holding four movies (Star Wars, Rogue One, The Clone Wars film, The
// Rise of Skywalker) and four Shows (The Mandalorian, Andor, Star Wars Rebels, The Clone
// Wars series). Change the list and the expected counts in TestIntegration_SyncTwice with it.
func fixtureListID(t *testing.T, key string) int64 {
	t.Helper()

	endpoint := mdblist.DefaultBaseURL + "/lists/user?" + url.Values{"apikey": {key}}.Encode()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, nil)
	must(t, err)

	req.Header.Set("User-Agent", "mdblistaar/test")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// The URL carries the API key, so report only the underlying error.
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}

		t.Fatalf("list the test account's lists: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list the test account's lists: HTTP %d", resp.StatusCode)
	}

	var lists []struct {
		ID int64 `json:"id"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&lists))

	if len(lists) != 1 {
		t.Fatalf("the test account must hold exactly one list, the fixture; it has %d", len(lists))
	}

	return lists[0].ID
}

// prepare checks the Arrs start empty, then adds the root folders and puts one movie and
// one Show from the fixture list on the Arrs' exclusion lists, which is where "prevent
// re-adding from import lists" goes when you delete a title.
func prepare(t *testing.T, r *radarr.Radarr, s *sonarr.Sonarr, items mdblist.Items) {
	t.Helper()

	ctx := t.Context()

	movies, err := r.GetMovieContext(ctx, &radarr.GetMovie{ExcludeLocalCovers: true})
	must(t, err)

	series, err := s.GetAllSeriesContext(ctx)
	must(t, err)

	if len(movies)+len(series) != 0 {
		t.Fatal("the Arrs aren't empty: run `mise run test:integration`, which recreates them")
	}

	for _, want := range []struct {
		items []mdblist.Item
		id    func(mdblist.Item) int64
		value int64
	}{
		{items.Movies, func(i mdblist.Item) int64 { return i.TMDBID }, blockedMovieTMDB},
		{items.Movies, func(i mdblist.Item) int64 { return i.TMDBID }, cloneWarsFilm},
		{items.Shows, func(i mdblist.Item) int64 { return i.TVDBID }, blockedShowTVDB},
	} {
		if !slices.ContainsFunc(want.items, func(i mdblist.Item) bool { return want.id(i) == want.value }) {
			t.Fatalf("the fixture list is missing title %d", want.value)
		}
	}

	_, err = r.AddRootFolderContext(ctx, &radarr.RootFolder{Path: "/movies"})
	must(t, err)

	_, err = s.AddRootFolderContext(ctx, &sonarr.RootFolder{Path: "/tv"})
	must(t, err)

	must(t, r.AddExclusionsContext(ctx, []*radarr.Exclusion{
		{TMDBID: blockedMovieTMDB, Title: "Star Wars: The Rise of Skywalker", Year: 2019},
	}))

	_, err = s.AddExclusionContext(ctx, &sonarr.Exclusion{TVDBID: blockedShowTVDB, Title: "Star Wars: The Clone Wars"})
	must(t, err)
}

func assertMovies(t *testing.T, r *radarr.Radarr, listed []mdblist.Item, excluded int64) {
	t.Helper()

	ctx := t.Context()

	library, err := r.GetMovieContext(ctx, &radarr.GetMovie{ExcludeLocalCovers: true})
	must(t, err)

	profiles, err := r.GetQualityProfilesContext(ctx)
	must(t, err)

	tags, err := r.GetTagsContext(ctx)
	must(t, err)

	anyID := profiles[slices.IndexFunc(profiles, func(p *radarr.QualityProfile) bool { return p.Name == "Any" })].ID
	tagID := tags[slices.IndexFunc(tags, func(tag *starr.Tag) bool { return tag.Label == "mdblistaar-it" })].ID

	byID := make(map[int64]*radarr.Movie, len(library))
	for _, m := range library {
		byID[m.TmdbID] = m
	}

	if want := len(listed) - 1; len(library) != want {
		t.Errorf("radarr has %d movies, want %d", len(library), want)
	}

	for _, item := range listed {
		m, found := byID[item.TMDBID]

		switch {
		case item.TMDBID == excluded:
			if found {
				t.Errorf("excluded movie %d %q was added", item.TMDBID, item.Title)
			}
		case !found:
			t.Errorf("movie %d %q was not added", item.TMDBID, item.Title)
		case !strings.HasPrefix(m.Path, "/movies/") || m.QualityProfileID != anyID || !m.Monitored ||
			m.MinimumAvailability != "released" || !slices.Contains(m.Tags, tagID):
			t.Errorf("movie %q added with wrong options: path %q profile %d monitored %t availability %q tags %v",
				m.Title, m.Path, m.QualityProfileID, m.Monitored, m.MinimumAvailability, m.Tags)
		}
	}
}

func assertShows(t *testing.T, s *sonarr.Sonarr, listed []mdblist.Item, excluded int64) {
	t.Helper()

	ctx := t.Context()

	library, err := s.GetAllSeriesContext(ctx)
	must(t, err)

	profiles, err := s.GetQualityProfilesContext(ctx)
	must(t, err)

	tags, err := s.GetTagsContext(ctx)
	must(t, err)

	anyID := profiles[slices.IndexFunc(profiles, func(p *sonarr.QualityProfile) bool { return p.Name == "Any" })].ID
	tagID := tags[slices.IndexFunc(tags, func(tag *starr.Tag) bool { return tag.Label == "mdblistaar-it" })].ID

	byID := make(map[int64]*sonarr.Series, len(library))
	for _, sr := range library {
		byID[sr.TvdbID] = sr
	}

	if want := len(listed) - 1; len(library) != want {
		t.Errorf("sonarr has %d shows, want %d", len(library), want)
	}

	for _, item := range listed {
		sr, found := byID[item.TVDBID]

		switch {
		case item.TVDBID == excluded:
			if found {
				t.Errorf("excluded show %d %q was added", item.TVDBID, item.Title)
			}
		case !found:
			t.Errorf("show %d %q was not added", item.TVDBID, item.Title)
		case !strings.HasPrefix(sr.Path, "/tv/") || sr.QualityProfileID != anyID || !sr.Monitored ||
			sr.SeriesType != "standard" || !sr.SeasonFolder || !slices.Contains(sr.Tags, tagID):
			t.Errorf(
				"show %q added with wrong options: path %q profile %d monitored %t type %q seasonFolder %t tags %v",
				sr.Title,
				sr.Path,
				sr.QualityProfileID,
				sr.Monitored,
				sr.SeriesType,
				sr.SeasonFolder,
				sr.Tags,
			)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}
