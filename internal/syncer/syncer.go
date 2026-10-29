// Package syncer runs a Sync: it adds each Source List's missing movies to Radarr
// and its missing Shows to Sonarr. It only ever adds.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"

	"golift.io/starr"
	"golift.io/starr/radarr"
	"golift.io/starr/sonarr"

	"github.com/arsac/mdblistaar/internal/config"
	"github.com/arsac/mdblistaar/internal/mdblist"
)

// Syncer runs Syncs.
type Syncer struct {
	log    *slog.Logger
	lists  *mdblist.Client
	radarr *radarr.Radarr
	sonarr *sonarr.Sonarr
	dryRun bool
}

// New returns a Syncer. With dryRun it logs the adds it would make and makes none.
func New(log *slog.Logger, lists *mdblist.Client, r *radarr.Radarr, s *sonarr.Sonarr, dryRun bool) *Syncer {
	return &Syncer{log: log, lists: lists, radarr: r, sonarr: s, dryRun: dryRun}
}

// arr is the state of one Arr that a Sync works against.
type arr struct {
	name     string
	library  map[int64]bool
	excluded map[int64]bool
	profiles map[string]int64
	roots    []string
	tags     map[string]int
	addTag   func(context.Context, *starr.Tag) (*starr.Tag, error)
	// items picks this Arr's half of a Source List, and id a title's id in this Arr.
	items func(mdblist.Items) []mdblist.Item
	id    func(mdblist.Item) int64
	// adder resolves a Source List's Add Options for this Arr.
	adder func(context.Context, config.SourceList) (addFunc, error)
}

// addFunc adds the title with the given Arr-native id.
type addFunc func(ctx context.Context, id int64) error

// Run does one Sync over sourceLists. A failure skips only the Source List, Arr or
// title it affects; Run carries on and returns every failure joined.
func (s *Syncer) Run(ctx context.Context, sourceLists []config.SourceList) error {
	var (
		errs []error
		arrs []*arr
	)

	for _, load := range []func(context.Context) (*arr, error){s.loadRadarr, s.loadSonarr} {
		a, err := load(ctx)
		if err != nil {
			s.log.ErrorContext(ctx, "arr unavailable, skipping it", "error", err)
			errs = append(errs, err)

			continue
		}

		arrs = append(arrs, a)
	}

	// With no Arr to add to, fetching Source Lists would only spend mdblist quota.
	if len(arrs) == 0 {
		return errors.Join(errs...)
	}

	for _, list := range sourceLists {
		log := s.log.With("list", list.Name)

		items, err := s.lists.ListItems(ctx, list.ID)
		if err != nil {
			log.ErrorContext(ctx, "fetch source list failed", "error", err)
			errs = append(errs, fmt.Errorf("source list %q: %w", list.Name, err))

			continue
		}

		for _, a := range arrs {
			errs = append(errs, s.syncList(ctx, log.With("arr", a.name), a, list, items))
		}
	}

	return errors.Join(errs...)
}

// syncList adds each of a Source List's titles for one Arr that is neither in its
// library nor excluded.
func (s *Syncer) syncList(
	ctx context.Context,
	log *slog.Logger,
	a *arr,
	list config.SourceList,
	items mdblist.Items,
) error {
	add, err := a.adder(ctx, list)
	if err != nil {
		log.ErrorContext(ctx, "invalid add options, skipping", "error", err)
		return fmt.Errorf("%s: %w", a.name, err)
	}

	var (
		errs                                    []error
		added, existing, excluded, noID, failed int
	)

	for _, item := range a.items(items) {
		id := a.id(item)
		itemLog := log.With("title", item.Title, "year", item.Year, "id", id)

		switch {
		case id == 0:
			noID++

			itemLog.WarnContext(ctx, "no id for this arr, skipping")
		case a.library[id]:
			existing++
		case a.excluded[id]:
			excluded++
		default:
			if !s.dryRun {
				if addErr := add(ctx, id); addErr != nil {
					failed++

					itemLog.WarnContext(ctx, "add failed", "error", addErr)
					errs = append(errs, fmt.Errorf("%s: add %q (%d): %w", a.name, item.Title, id, addErr))

					continue
				}
			}

			added++
			// Later Source Lists now see it as existing, so the first list to add a title wins.
			a.library[id] = true

			itemLog.InfoContext(ctx, "added", "dry_run", s.dryRun)
		}
	}

	log.InfoContext(ctx, "synced",
		"added", added, "existing", existing, "excluded", excluded, "no_id", noID, "failed", failed)

	return errors.Join(errs...)
}

func (s *Syncer) loadRadarr(ctx context.Context) (*arr, error) {
	profiles, err := s.radarr.GetQualityProfilesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("radarr: quality profiles: %w", err)
	}

	roots, err := s.radarr.GetRootFoldersContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("radarr: root folders: %w", err)
	}

	tags, err := s.radarr.GetTagsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("radarr: tags: %w", err)
	}

	movies, err := s.radarr.GetMovieContext(ctx, &radarr.GetMovie{ExcludeLocalCovers: true})
	if err != nil {
		return nil, fmt.Errorf("radarr: movies: %w", err)
	}

	exclusions, err := s.radarr.GetExclusionsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("radarr: exclusions: %w", err)
	}

	a := &arr{
		name:     "radarr",
		library:  make(map[int64]bool, len(movies)),
		excluded: make(map[int64]bool, len(exclusions)),
		profiles: make(map[string]int64, len(profiles)),
		tags:     tagIDs(tags),
		addTag:   s.radarr.AddTagContext,
		items:    func(i mdblist.Items) []mdblist.Item { return i.Movies },
		id:       func(i mdblist.Item) int64 { return i.TMDBID },
	}
	a.adder = func(ctx context.Context, l config.SourceList) (addFunc, error) {
		return s.radarrAdder(ctx, a, l.Radarr)
	}

	for _, p := range profiles {
		a.profiles[p.Name] = p.ID
	}

	for _, r := range roots {
		a.roots = append(a.roots, r.Path)
	}

	for _, m := range movies {
		a.library[m.TmdbID] = true
	}

	for _, e := range exclusions {
		a.excluded[e.TMDBID] = true
	}

	return a, nil
}

func (s *Syncer) loadSonarr(ctx context.Context) (*arr, error) {
	profiles, err := s.sonarr.GetQualityProfilesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("sonarr: quality profiles: %w", err)
	}

	roots, err := s.sonarr.GetRootFoldersContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("sonarr: root folders: %w", err)
	}

	tags, err := s.sonarr.GetTagsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("sonarr: tags: %w", err)
	}

	series, err := s.sonarr.GetAllSeriesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("sonarr: series: %w", err)
	}

	exclusions, err := s.sonarr.GetExclusionsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("sonarr: exclusions: %w", err)
	}

	a := &arr{
		name:     "sonarr",
		library:  make(map[int64]bool, len(series)),
		excluded: make(map[int64]bool, len(exclusions)),
		profiles: make(map[string]int64, len(profiles)),
		tags:     tagIDs(tags),
		addTag:   s.sonarr.AddTagContext,
		items:    func(i mdblist.Items) []mdblist.Item { return i.Shows },
		id:       func(i mdblist.Item) int64 { return i.TVDBID },
	}
	a.adder = func(ctx context.Context, l config.SourceList) (addFunc, error) {
		return s.sonarrAdder(ctx, a, l.Sonarr)
	}

	for _, p := range profiles {
		a.profiles[p.Name] = p.ID
	}

	for _, r := range roots {
		a.roots = append(a.roots, r.Path)
	}

	for _, sr := range series {
		a.library[sr.TvdbID] = true
	}

	for _, e := range exclusions {
		a.excluded[e.TVDBID] = true
	}

	return a, nil
}

func (s *Syncer) radarrAdder(ctx context.Context, a *arr, opts config.RadarrOptions) (addFunc, error) {
	profileID, rootFolder, tagIDs, err := s.resolve(ctx, a, opts.QualityProfile, opts.RootFolder, opts.Tags)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, tmdbID int64) error {
		movie, lookupErr := s.radarr.LookupTMDBContext(ctx, tmdbID)
		if lookupErr != nil {
			return fmt.Errorf("lookup: %w", lookupErr)
		}

		_, addErr := s.radarr.AddMovieContext(ctx, &radarr.AddMovieInput{
			Title:               movie.Title,
			TitleSlug:           movie.TitleSlug,
			Year:                movie.Year,
			Images:              movie.Images,
			TmdbID:              tmdbID,
			QualityProfileID:    profileID,
			RootFolderPath:      rootFolder,
			MinimumAvailability: radarr.Availability(opts.MinimumAvailability),
			Monitored:           opts.Monitor != "none",
			Tags:                tagIDs,
			AddOptions: &radarr.AddMovieOptions{
				SearchForMovie: *opts.SearchOnAdd,
				Monitor:        opts.Monitor,
			},
		})
		if addErr != nil {
			return fmt.Errorf("add: %w", addErr)
		}

		return nil
	}, nil
}

func (s *Syncer) sonarrAdder(ctx context.Context, a *arr, opts config.SonarrOptions) (addFunc, error) {
	profileID, rootFolder, tagIDs, err := s.resolve(ctx, a, opts.QualityProfile, opts.RootFolder, opts.Tags)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, tvdbID int64) error {
		found, lookupErr := s.sonarr.GetSeriesLookupContext(ctx, "", tvdbID)
		if lookupErr != nil {
			return fmt.Errorf("lookup: %w", lookupErr)
		}

		i := slices.IndexFunc(found, func(f *sonarr.Series) bool { return f.TvdbID == tvdbID })
		if i < 0 {
			return fmt.Errorf("lookup: tvdb id %d not found", tvdbID)
		}

		series := found[i]

		_, addErr := s.sonarr.AddSeriesContext(ctx, &sonarr.AddSeriesInput{
			Title:            series.Title,
			TitleSlug:        series.TitleSlug,
			Images:           series.Images,
			Seasons:          series.Seasons,
			TvdbID:           tvdbID,
			QualityProfileID: profileID,
			RootFolderPath:   rootFolder,
			SeriesType:       opts.SeriesType,
			SeasonFolder:     *opts.SeasonFolder,
			Monitored:        opts.Monitor != "none",
			Tags:             tagIDs,
			AddOptions: &sonarr.AddSeriesOptions{
				SearchForMissingEpisodes: *opts.SearchOnAdd,
				Monitor:                  sonarr.MonitorType(opts.Monitor),
			},
		})
		if addErr != nil {
			return fmt.Errorf("add: %w", addErr)
		}

		return nil
	}, nil
}

// resolve turns a Source List's named profile, root folder and tags into what the
// Arr's API takes, creating any tag the Arr doesn't have yet.
func (s *Syncer) resolve(
	ctx context.Context,
	a *arr,
	profile, rootFolder string,
	tags []string,
) (int64, string, []int, error) {
	profileID, ok := a.profiles[profile]
	if !ok {
		return 0, "", nil, fmt.Errorf("quality profile %q not found", profile)
	}

	i := slices.IndexFunc(a.roots, func(r string) bool { return path.Clean(r) == path.Clean(rootFolder) })
	if i < 0 {
		return 0, "", nil, fmt.Errorf("root folder %q not found", rootFolder)
	}

	ids := make([]int, 0, len(tags))

	for _, label := range tags {
		if id, found := a.tags[strings.ToLower(label)]; found {
			ids = append(ids, id)
			continue
		}

		if s.dryRun {
			s.log.InfoContext(ctx, "dry run: would create tag", "arr", a.name, "tag", label)
			continue
		}

		tag, err := a.addTag(ctx, &starr.Tag{Label: label})
		if err != nil {
			return 0, "", nil, fmt.Errorf("create tag %q: %w", label, err)
		}

		a.tags[strings.ToLower(tag.Label)] = tag.ID
		ids = append(ids, tag.ID)
	}

	return profileID, a.roots[i], ids, nil
}

// tagIDs maps lowercased tag labels to ids; the Arrs treat labels case-insensitively.
func tagIDs(tags []*starr.Tag) map[string]int {
	ids := make(map[string]int, len(tags))
	for _, t := range tags {
		ids[strings.ToLower(t.Label)] = t.ID
	}

	return ids
}
