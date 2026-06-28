# series-linker

Pipeline CLI that takes a TV show name, loads all episodes from TVmaze, searches Webshare for every episode, scores candidates, and emits one download URL per episode when a good match is found.

Use this only for files you are allowed to access and download.

## Run

```sh
cd /home/adam/workspace/personal/umbrel-apps

set -a
source webshare-search/.env
set +a

go run ./series-linker "Pripady 1 oddeleni"
```

Build and run:

```sh
cd /home/adam/workspace/personal/umbrel-apps/series-linker
go build -o series-linker .
./series-linker "Pripady 1 oddeleni"
```

JSON with candidate audit:

```sh
./series-linker -format json -candidates 5 "Pripady 1 oddeleni"
```

Useful flags:

- `-search-limit 8`: Webshare results fetched for each generated query.
- `-candidates 5`: keep top candidate details in JSON.
- `-min-score 0.35`: minimum score required before generating a URL.
- `-show-id 13257`: skip TVmaze show search.
- `-wst`, `-username`, `-password`: same login options as `webshare-search`.

Default TSV columns:

- `show`
- `code`
- `episode_name`
- `webshare_name`
- `size`
- `score`
- `url`
- `error`
