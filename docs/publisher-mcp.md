# Publisher MCP contract

The **MCP publisher** connector (`mcp_publisher`) posts approved posts to Threads, Instagram and YouTube accounts through
any MCP server that provides the tools below. CueFlow keeps the review, the approval and the retries; the server
owns the platforms (logins, tokens, the publish calls). If you run your own scheduler, giving it these four tools
is all it takes to be a publisher for CueFlow. [Postiz](https://github.com/gitroomhq/postiz-app) has its own
connector (`postiz_mcp`) because its tools take another shape.

```
CueFlow ──MCP (Streamable HTTP)──▶ your server ──▶ Threads · Instagram
  review → approve → dispatch         list_accounts · create_post · publish_post · create_upload
```

## Settings

| Setting | Meaning |
|---|---|
| MCP endpoint | The server's URL, usually ending in `/mcp` (Streamable HTTP; stateless is fine) |
| Bearer token | Sent as `Authorization: Bearer <token>` |
| Extra headers | Plain headers on every request |
| Secret headers | `Header-Name: value`, one per line, stored encrypted. For a server behind Cloudflare Access: `CF-Access-Client-Id` and `CF-Access-Client-Secret` of a service token |
| Tool names | `list_accounts`, `create_post`, `publish_post`, `create_upload` by default; change them if your server calls them something else |
| Public media URL | Optional. Where the server can fetch a video CueFlow stores, if it has no upload tool |

Connectors made while this was the CueSocial connector (type `cuesocial`, with `cf_client_id` and
`cf_client_secret`) keep working unchanged.

## Tools

### `list_accounts` `{}`
The accounts a post can go to; the review editor lists them and the reviewer ticks the ones for each post.

```json
{"accounts": [{"id": "th1", "platform": "threads", "username": "brand", "tag": "brand"}],
 "platforms": [{"platform": "threads", "configured": true}]}
```

`platform` is `threads`, `instagram` or `youtube`. `tag` is free text (`brand`, `personal`, `test`...); an account tagged
`test` is never posted together with accounts that are not, so a test run cannot reach a real audience.

### `create_post`
```json
{"parts": [{"text": "…", "image_urls": ["https://…"], "images_base64": ["…"], "video_urls": ["https://…"], "video_keys": ["uploads/…"]}],
 "accounts": ["th1", "ig1"], "scheduled_at": "2026-10-09T09:00:00Z", "topic": "AI Threads", "link": "https://…"}
```

- `parts` are thread parts in order (a reply chain on Threads; one caption and carousel on Instagram). CueFlow
  appends the hashtags to the last part and puts every image and video on the **first** part.
- Within a part: linked images, then uploaded (base64) images, then videos.
- On **YouTube** the post is a Short: exactly one video (vertical or square, up to 3 minutes) and no images.
  The first line of the text is the title (cut at 100 characters), the rest the description; neither may contain `<` or `>`.
  The server turns the parts into one post, like Instagram.
- `scheduled_at` is sent only for a time in the future; otherwise the post goes out now.
- `topic` (a Threads topic tag) and `link` (a link preview card, which needs a part without images) are sent only
  when a Threads account is chosen.

Reply:
```json
{"id": "post-1", "status": "published",
 "results": [{"account": "th1", "label": "threads @brand", "published": true, "permalink": "https://…", "error": ""}]}
```
`status` is `published`, `scheduled` or `publishing` for success; anything else (`partial`, `failed`) is a failure
that CueFlow keeps the post `id` for. A failed **tool call** (an MCP error result) means the server refused the post
and nothing went out.

### `publish_post` `{"id": "post-1"}`
Retries a post that was created but did not reach every account, republishing **only the accounts that failed**.
Same reply as `create_post`.

### `create_upload` `{"content_type": "video/mp4", "size": 123}` (optional)
For videos CueFlow stores. Returns a signed URL to PUT the bytes to:
```json
{"upload_url": "https://…", "method": "PUT", "headers": {"Content-Type": "video/mp4"}, "key": "uploads/…"}
```
The key then goes into a part's `video_keys`. A server without this tool (or one that refuses) can instead fetch
videos from the connector's public media URL.

## What CueFlow checks before it calls

So a post that cannot work never reaches the server: at least one account, every account listed and still
connected, each account's platform among the post's platforms, no mix of `test` accounts with others; Threads
parts of at most 500 characters and a topic of at most 50 (no `.` or `&`); an Instagram caption of at most 2,200
characters and at least one image or video; a YouTube post of exactly one video, no images, and no `<` or `>` in
the text; at most 10 parts, 20 media on Threads and 10 on Instagram; stored
images JPEG or PNG up to 8 MB; linked media on a public `https` address.

## Retries

The contract has no idempotency key. If `create_post` gets no reply (a timeout, a dropped connection) the post may
or may not have gone out, so CueFlow reports a failure that says so and leaves the choice to the reviewer. If the
reply says some accounts failed, the post `id` is kept and **Retry** calls `publish_post`, never `create_post`
again, so no account receives a second copy.
