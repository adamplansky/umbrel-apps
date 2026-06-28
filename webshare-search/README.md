# webshare-search

Go CLI for searching Webshare and generating download URLs for the returned files.

Use this only for files you are allowed to access and download.

## Run

```sh
cd /home/adam/webshare-search
go run . "ubuntu iso"
```

Or use the built binary:

```sh
cd /home/adam/webshare-search
./webshare-search "ubuntu iso"
```

Only the first result:

```sh
./webshare-search -first "ubuntu iso"
```

JSON output:

```sh
./webshare-search -format json -limit 5 "ubuntu iso"
```

CSV output:

```sh
./webshare-search -format csv -limit 20 "ubuntu iso" > links.csv
```

## Webshare Login

For fast/VIP download URLs, log in before generating links.

Create a local `.env` file from the template:

```sh
cd /home/adam/webshare-search
cp .env.example .env
```

Edit `.env` and fill in:

```sh
WEBSHARE_USERNAME="your-email-or-username"
WEBSHARE_PASSWORD="your-password"
```

Export it into your current shell:

```sh
set -a
source .env
set +a
```

Then run:

```sh
./webshare-search -first "ubuntu iso"
```

You can also pass an existing Webshare session token:

```sh
WEBSHARE_WST='your-webshare-token'
./webshare-search -limit 5 "ubuntu iso"
```

Flags are available too:

```sh
./webshare-search -username 'your-email-or-username' -password 'your-password' -first "ubuntu iso"
./webshare-search -wst 'your-webshare-token' -first "ubuntu iso"
```

The password is only used in memory to create the Webshare API auth hash.

## Output

Default output is TSV with:

- `ident`
- `name`
- `type`
- `size`
- `size_human`
- votes
- password flag
- generated download `url`
- per-row `error`

Password-protected files are skipped by default. Use `-include-password` if you still want the tool to call `file_link` for them.
