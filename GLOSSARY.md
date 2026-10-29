# mdblistaar

Reads mdblist.com lists and adds their movies to Radarr and their shows to Sonarr.

## Language

**Source List**:
A list on mdblist.com that mdblistaar reads from. It may mix movies and shows.
_Avoid_: mdblist, upstream list, input list

**Show**:
A TV title; the only kind of item that goes to Sonarr.
_Avoid_: series, tv

**Sync**:
One run over every Source List, adding each missing movie to Radarr and each missing Show to Sonarr.
_Avoid_: import, refresh, push

**Add Options**:
The settings a Source List's titles are added to an Arr with, such as root folder, quality profile and tags. Each Source List has its own Add Options per Arr.
_Avoid_: defaults, profile, list settings
