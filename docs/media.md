# Images and videos

Posts reference media as `/media/{id}` (images up to 10 MB; MP4 up to 100 MB). Media gets there in three ways:
uploaded from the review editor, generated with an image connector (by a reviewer, or by an agent with the
`imaging` capability), or attached by an agent from what a tool produced (for example a rendered video).

## Where the bytes live

`CUEFLOW_MEDIA_STORAGE` decides where new files are kept:

| Value | Storage |
|---|---|
| empty | the database (fine for a small install) |
| `file:///data/media` | a directory, e.g. a Docker volume |
| `gcs://bucket/prefix` | Google Cloud Storage; `/media/{id}` answers with a short-lived signed URL (IAM-signed, no key file) |

`cueflow media migrate --to <target>` moves existing media between backends; URLs do not change, because each
row records which backend holds its bytes.

When handing a post to a publisher, CueFlow turns `/media/{id}` into an absolute URL under
`CUEFLOW_MEDIA_BASE_URL`, which must be reachable from the publisher (Compose sets it to `http://cueflow:3000`).

## Deleting

- `DELETE /api/v1/media/{id}` removes the item and its file, and answers `409` while a post that is not yet
  published or rejected still uses it.
- `cueflow media prune --older-than 90d [--dry-run]` does the same for everything old and unused.
- `CUEFLOW_MEDIA_RETENTION_DAYS` runs that prune on every tick in cloud mode.

## Big files from a trusted service

A service of yours that produces big files (a video renderer, for example) should not push them through
CueFlow. With GCS storage it can write straight into the bucket's `uploads/` area with its own Google identity
and have CueFlow adopt the file with `POST /internal/media/register`; the bytes never pass through CueFlow.
The account must be listed in `CUEFLOW_MEDIA_REGISTER_ACCOUNTS`. Details: [api.md](api.md#media) and
[deploy/cloudrun/README.md](../deploy/cloudrun/README.md#services-that-write-media-to-the-bucket).
