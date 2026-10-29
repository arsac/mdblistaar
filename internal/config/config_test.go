package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsac/mdblistaar/internal/config"
)

const configHead = `radarr:
  url: http://radarr:7878
sonarr:
  url: http://sonarr:8989
sourceLists:
`

const animeList = `  - name: anime
    id: 12345
    radarr:
      rootFolderPath: /movies/anime
      qualityProfile: Remux-1080p - Anime
      tags: [mdblist]
      monitor: movieOnly
      searchForMovie: true
      minimumAvailability: released
    sonarr:
      rootFolderPath: /tv/anime
      qualityProfile: Remux-1080p - Anime
      monitor: all
      searchForMissingEpisodes: false
      seriesType: anime
      seasonFolder: true
`

const validConfig = configHead + animeList

func env(t *testing.T, configYAML string, vars map[string]string) func(string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	all := map[string]string{
		"CONFIG_PATH":     path,
		"MDBLIST_API_KEY": "mdb-key",
		"RADARR_API_KEY":  "radarr-key",
		"SONARR_API_KEY":  "sonarr-key",
	}
	maps.Copy(all, vars)

	return func(key string) string { return all[key] }
}

func TestLoad_Valid(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(t, validConfig, map[string]string{"DRY_RUN": "true"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.DryRun {
		t.Error("DryRun = false, want true")
	}

	if cfg.Radarr.URL != "http://radarr:7878" || cfg.Radarr.APIKey != "radarr-key" {
		t.Errorf("Radarr = %+v", cfg.Radarr)
	}

	if cfg.Sonarr.APIKey != "sonarr-key" || cfg.MDBListAPIKey != "mdb-key" {
		t.Errorf("keys not read from env: sonarr %q, mdblist %q", cfg.Sonarr.APIKey, cfg.MDBListAPIKey)
	}

	if len(cfg.SourceLists) != 1 {
		t.Fatalf("SourceLists = %d, want 1", len(cfg.SourceLists))
	}

	l := cfg.SourceLists[0]
	if l.ID != 12345 || l.Sonarr.SeriesType != "anime" || !*l.Sonarr.SeasonFolder || !*l.Radarr.SearchForMovie {
		t.Errorf("SourceList = %+v", l)
	}
}

func TestLoad_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   string
		vars     map[string]string
		wantErrs []string
	}{
		{
			name:   "missing secrets",
			config: validConfig,
			vars:   map[string]string{"MDBLIST_API_KEY": "", "RADARR_API_KEY": "", "SONARR_API_KEY": ""},
			wantErrs: []string{
				"MDBLIST_API_KEY is required",
				"RADARR_API_KEY is required",
				"SONARR_API_KEY is required",
			},
		},
		{
			name:     "bad dry run",
			config:   validConfig,
			vars:     map[string]string{"DRY_RUN": "maybe"},
			wantErrs: []string{"DRY_RUN"},
		},
		{
			name:     "unknown field",
			config:   strings.Replace(validConfig, "rootFolderPath:", "rootFolderPaths:", 1),
			wantErrs: []string{"field rootFolderPaths not found"},
		},
		{
			name:     "bad url",
			config:   strings.Replace(validConfig, "http://radarr:7878", "radarr:7878", 1),
			wantErrs: []string{"radarr.url"},
		},
		{
			name:     "no source lists",
			config:   configHead + "  []\n",
			wantErrs: []string{"at least one Source List"},
		},
		{
			name: "bad add options",
			config: strings.NewReplacer(
				"seriesType: anime", "seriesType: cartoon",
				"monitor: movieOnly", "monitor: all",
				"seasonFolder: true", "seasonFolder: null",
				"qualityProfile: Remux-1080p - Anime\n      tags", "tags",
			).Replace(validConfig),
			wantErrs: []string{
				`"anime" sonarr.seriesType must be one of`,
				`"anime" radarr.monitor must be one of`,
				`"anime" sonarr.seasonFolder is required`,
				`"anime" radarr.qualityProfile is required`,
			},
		},
		{
			name:     "bad id",
			config:   strings.Replace(validConfig, "id: 12345", "id: 0", 1),
			wantErrs: []string{"id must be a positive"},
		},
		{
			name:     "duplicate names",
			config:   validConfig + animeList,
			wantErrs: []string{`"anime": duplicate name`},
		},
		{
			name:     "missing file",
			config:   validConfig,
			vars:     map[string]string{"CONFIG_PATH": "/nonexistent/config.yaml"},
			wantErrs: []string{"read config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(env(t, tt.config, tt.vars))
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}

			for _, want := range tt.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error missing %q:\n%v", want, err)
				}
			}
		})
	}
}
