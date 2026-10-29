# Push titles to the Arrs instead of serving Import Lists

mdblist's own docs point Sonarr and Radarr Import Lists at a list's export, but neither Arr knows a list's media type: Radarr's Custom List reads every TMDb id as a movie (a show's TV id resolves to an unrelated movie), and Sonarr reads every TVDB id as a series. Rather than serve per-media-type lists for the Arrs to pull, mdblistaar runs as a CronJob that reads each Source List once from the documented API and adds movies to Radarr and Shows to Sonarr through their APIs, so routing by media type is owned in one place.

## Consequences

- Add Options (root folder, quality profile, tags, monitoring, search) live in mdblistaar's config, not in each Arr's Import List settings.
- A Sync only adds. It skips titles already in the library or on the Arr's exclusion list and never modifies or removes anything, so the Arrs' list-cleaning features don't apply. Stopping a title from coming back means deleting it in the Arr with "add exclusion" ticked.
