# csfdmeta

Small CLI for exporting searchable TV episode metadata without scraping CSFD.

It uses the public TVmaze REST API:

- `GET /search/shows?q=:query`
- `GET /shows/:id/episodes`

TVmaze is public, JSON based, and does not require an API key for this workflow.

## Usage

```sh
go run . "Pripady 1 oddeleni"
```

Default output is TSV, which is easy to search:

```sh
go run . "Pripady 1 oddeleni" | rg "S01|Vražda"
```

JSON output:

```sh
go run . -format json "Pripady 1 oddeleni"
```

If you already know the TVmaze show ID:

```sh
go run . -show-id 42
```

Include specials:

```sh
go run . -specials "Pripady 1 oddeleni"
```

## Why not CSFD

CSFD does not provide a documented public stable API for this use case. TVmaze gives us stable endpoints for show search and episode lists, so the tool can avoid scraping HTML.
