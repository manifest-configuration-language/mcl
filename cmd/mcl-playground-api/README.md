# Playground Share API

The service stores immutable Playground snapshots and returns them to the browser. It does not evaluate MCL or call the Kubernetes API.

## HTTP API

`POST /api/playground/snippets` accepts a JSON snapshot and returns an identifier and expiration time.

```json
{
  "version": 1,
  "source": "emit resource",
  "files": {
    "values.yaml": { "encoding": "utf8", "content": "replicas: 2\n" },
    "archive.bin": { "encoding": "binary", "content": "AAECAw" }
  },
  "activePath": "main.mcl"
}
```

Binary file content uses unpadded base64url. `activePath` is optional and must name a UTF-8 project file or `main.mcl`. The response has the form `{"id":"<uuid>","expiresAt":"<timestamp>"}`.

`GET /api/playground/snippets/{id}` returns the stored snapshot. Expired and unknown identifiers both return `404`. The service retains snapshots for 90 days by default and limits aggregate snapshot payloads to 5 GiB.

## Configuration

`DATABASE_URL` is required. `LISTEN_ADDR` defaults to `:8080`; `PORT` is used when `LISTEN_ADDR` is unset. `SHARE_TTL` accepts a Go duration from one hour through 365 days. Request bodies are limited to 64 MiB, and aggregate stored snapshot payloads are limited to 5 GiB.

Each client IP can create 10 shares per hour with a burst of 3, and retrieve 60 shares per minute with a burst of 30. Rate limits are held in process memory.

`GET /healthz` reports process health. `GET /readyz` also checks the PostgreSQL connection.
