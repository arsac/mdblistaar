# mdblistaar

Adds the movies and shows from your [mdblist.com](https://mdblist.com) lists to Radarr and Sonarr. It only adds titles and never changes or deletes anything already in an Arr.

## Usage

Image: `ghcr.io/arsac/mdblistaar`

Each run syncs once, then exits. It exits non-zero if any part failed, so run it as a Kubernetes CronJob or on any other schedule.

### Environment

| Variable          | Description                                          |
| ----------------- | ---------------------------------------------------- |
| `MDBLIST_API_KEY` | mdblist API key (required)                           |
| `RADARR_API_KEY`  | Radarr API key (required)                            |
| `SONARR_API_KEY`  | Sonarr API key (required)                            |
| `CONFIG_PATH`     | Config file path, default `/config/config.yaml`      |
| `DRY_RUN`         | `true` logs what would be added without adding it    |

### Config

```yaml
radarr:
  url: http://radarr:7878
sonarr:
  url: http://sonarr:8989
sourceLists:
  - name: trending
    id: 12345
    radarr:
      rootFolder: /movies
      qualityProfile: HD-1080p
      tags: [mdblist]
      monitor: movieOnly
      searchOnAdd: true
      minimumAvailability: released
    sonarr:
      rootFolder: /tv
      qualityProfile: HD-1080p
      tags: [mdblist]
      monitor: all
      searchOnAdd: true
      seriesType: standard
      seasonFolder: true
```

`id` is the mdblist list id. Every field is required except `tags`. Quality profiles are referenced by name, and missing tags are created.
