// Package config loads and validates mdblistaar's configuration.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// DefaultPath is where the config file is read from when CONFIG_PATH is unset.
const DefaultPath = "/config/config.yaml"

// Config is everything a Sync needs.
type Config struct {
	MDBListAPIKey string       `yaml:"-"`
	DryRun        bool         `yaml:"-"`
	Radarr        Arr          `yaml:"radarr"`
	Sonarr        Arr          `yaml:"sonarr"`
	SourceLists   []SourceList `yaml:"sourceLists"`
}

// Arr is how to reach one Sonarr or Radarr instance.
type Arr struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"-"`
}

// SourceList is one mdblist.com list and the Add Options its titles get in each Arr.
type SourceList struct {
	Name   string        `yaml:"name"`
	ID     int64         `yaml:"id"`
	Radarr RadarrOptions `yaml:"radarr"`
	Sonarr SonarrOptions `yaml:"sonarr"`
}

// RadarrOptions are the Add Options for a Source List's movies.
type RadarrOptions struct {
	RootFolder          string   `yaml:"rootFolder"`
	QualityProfile      string   `yaml:"qualityProfile"`
	Tags                []string `yaml:"tags"`
	Monitor             string   `yaml:"monitor"`
	SearchOnAdd         *bool    `yaml:"searchOnAdd"`
	MinimumAvailability string   `yaml:"minimumAvailability"`
}

// SonarrOptions are the Add Options for a Source List's Shows.
type SonarrOptions struct {
	RootFolder     string   `yaml:"rootFolder"`
	QualityProfile string   `yaml:"qualityProfile"`
	Tags           []string `yaml:"tags"`
	Monitor        string   `yaml:"monitor"`
	SearchOnAdd    *bool    `yaml:"searchOnAdd"`
	SeriesType     string   `yaml:"seriesType"`
	SeasonFolder   *bool    `yaml:"seasonFolder"`
}

//nolint:gochecknoglobals // read-only lookup tables of the values each Arr accepts.
var (
	radarrMonitors       = []string{"movieOnly", "movieAndCollection", "none"}
	radarrAvailabilities = []string{"announced", "inCinemas", "released"}
	sonarrMonitors       = []string{
		"all", "future", "missing", "existing", "firstSeason", "lastSeason",
		"pilot", "recent", "monitorSpecials", "unmonitorSpecials", "none",
	}
	sonarrSeriesTypes = []string{"standard", "anime", "daily"}
)

// Load reads the config file named by CONFIG_PATH (default [DefaultPath]) and the
// secrets and flags from the environment, then validates the result.
func Load(getenv func(string) string) (*Config, error) {
	path := cmp.Or(getenv("CONFIG_PATH"), DefaultPath)

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	defer file.Close()

	var cfg Config

	dec := yaml.NewDecoder(file)
	dec.KnownFields(true)

	if err = dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.MDBListAPIKey = getenv("MDBLIST_API_KEY")
	cfg.Radarr.APIKey = getenv("RADARR_API_KEY")
	cfg.Sonarr.APIKey = getenv("SONARR_API_KEY")

	var errs []error

	if v := getenv("DRY_RUN"); v != "" {
		if cfg.DryRun, err = strconv.ParseBool(v); err != nil {
			errs = append(errs, fmt.Errorf("DRY_RUN: %w", err))
		}
	}

	if joined := errors.Join(append(errs, cfg.validate()...)...); joined != nil {
		return nil, fmt.Errorf("invalid config %s:\n%w", path, joined)
	}

	return &cfg, nil
}

func (c *Config) validate() []error {
	var errs []error

	if c.MDBListAPIKey == "" {
		errs = append(errs, errors.New("MDBLIST_API_KEY is required"))
	}

	errs = append(errs, validateArr("radarr", "RADARR_API_KEY", c.Radarr)...)
	errs = append(errs, validateArr("sonarr", "SONARR_API_KEY", c.Sonarr)...)

	if len(c.SourceLists) == 0 {
		errs = append(errs, errors.New("sourceLists: at least one Source List is required"))
	}

	seen := make(map[string]bool, len(c.SourceLists))

	for i, l := range c.SourceLists {
		at := fmt.Sprintf("sourceLists[%d]", i)
		if l.Name != "" {
			at = fmt.Sprintf("sourceLists[%d] %q", i, l.Name)
		}

		switch {
		case l.Name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", at))
		case seen[l.Name]:
			errs = append(errs, fmt.Errorf("%s: duplicate name", at))
		}

		seen[l.Name] = true

		if l.ID <= 0 {
			errs = append(errs, fmt.Errorf("%s: id must be a positive mdblist list id", at))
		}

		errs = append(errs, l.Radarr.validate(at+" radarr")...)
		errs = append(errs, l.Sonarr.validate(at+" sonarr")...)
	}

	return errs
}

func validateArr(name, keyEnv string, a Arr) []error {
	var errs []error

	if u, err := url.Parse(a.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("%s.url: must be an absolute http(s) URL, got %q", name, a.URL))
	}

	if a.APIKey == "" {
		errs = append(errs, fmt.Errorf("%s is required", keyEnv))
	}

	return errs
}

func (o RadarrOptions) validate(at string) []error {
	return slices.Concat(
		required(at, "rootFolder", o.RootFolder),
		required(at, "qualityProfile", o.QualityProfile),
		oneOf(at, "monitor", o.Monitor, radarrMonitors),
		oneOf(at, "minimumAvailability", o.MinimumAvailability, radarrAvailabilities),
		requiredBool(at, "searchOnAdd", o.SearchOnAdd),
	)
}

func (o SonarrOptions) validate(at string) []error {
	return slices.Concat(
		required(at, "rootFolder", o.RootFolder),
		required(at, "qualityProfile", o.QualityProfile),
		oneOf(at, "monitor", o.Monitor, sonarrMonitors),
		oneOf(at, "seriesType", o.SeriesType, sonarrSeriesTypes),
		requiredBool(at, "searchOnAdd", o.SearchOnAdd),
		requiredBool(at, "seasonFolder", o.SeasonFolder),
	)
}

func required(at, field, v string) []error {
	if v == "" {
		return []error{fmt.Errorf("%s.%s is required", at, field)}
	}

	return nil
}

func requiredBool(at, field string, v *bool) []error {
	if v == nil {
		return []error{fmt.Errorf("%s.%s is required", at, field)}
	}

	return nil
}

func oneOf(at, field, v string, allowed []string) []error {
	if !slices.Contains(allowed, v) {
		return []error{fmt.Errorf("%s.%s must be one of %v, got %q", at, field, allowed, v)}
	}

	return nil
}
