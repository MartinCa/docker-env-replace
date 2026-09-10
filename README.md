# docker-env-replace

A minimal Docker image utility that performs environment-variable token
replacement in files. It is designed to be used as a Docker Compose
**init container**: it reads a tree of template files from an input
directory and writes a fully substituted copy to an output directory,
then exits. If any referenced variable is missing, the run fails, the
previous output is left untouched, and the container exits non-zero so
that dependent services wait for a *successful* run.

Implemented in [Go](https://go.dev) using only the standard library.
No external dependencies.

## How it works

- Every file and directory under `ENVREPLACE_INPUT_DIR` (default
  `/input`) is mirrored to `ENVREPLACE_OUTPUT_DIR` (default `/output`)
  with an identical structure.
- Inside text files, each token

  ```
  <NAME>
  ```

  is replaced by the value of the environment variable `NAME`. The
  default token delimiters `<` and `>` are configurable.
- The input tree is **never written to**; the utility only reads it.

### Configuration

All utility configuration uses the `ENVREPLACE_` prefix. A config
variable that is unset *or* set to the empty string falls back to its
default — except `ENVREPLACE_TOKEN_PREFIX` and
`ENVREPLACE_TOKEN_SUFFIX`, which must always be non-empty: setting
either to the empty string is a configuration error. Any *other*
environment variable is a candidate replacement variable — including
variables whose names start with `ENVREPLACE_`: a token such as
`<ENVREPLACE_CUSTOM_FLAVOR>` simply resolves the environment variable
of that name, exactly like any other token.

| Variable                    | Default  | Description                                             |
|-----------------------------|----------|---------------------------------------------------------|
| `ENVREPLACE_INPUT_DIR`      | `/input` | Directory tree to process (must exist and be a directory) |
| `ENVREPLACE_OUTPUT_DIR`     | `/output`| Directory to write the substituted tree to               |
| `ENVREPLACE_TOKEN_PREFIX`   | `<`      | Token start delimiter                                   |
| `ENVREPLACE_TOKEN_SUFFIX`   | `>`      | Token end delimiter                                     |
| `ENVREPLACE_EMPTY_VALUE`    | `(empty)`| Sentinel value that expands to the empty string         |

### Behavior rules

1. **Tokens.** A token is `PREFIX + NAME + SUFFIX`, where `NAME` is any
   non-empty byte sequence. Replacement scans the raw bytes of each text
   file, so content, line endings, and all other bytes are preserved
   exactly (a file is never re-flowed or re-encoded).
2. **Missing variables are errors.** If a token names a variable that is
   not present in the environment, processing stops at the first such
   token and the run fails, identifying the input file path and the
   variable name (values are never logged).
3. **Set-but-empty is an error too.** A variable that is present but set
   to the empty string (and is not the empty sentinel) fails the same
   way. To write an intentionally empty value, set the variable to the
   empty sentinel instead (default `(empty)`).
4. **Empty sentinel.** If the referenced value is exactly
   `ENVREPLACE_EMPTY_VALUE`, it expands to the empty string.
5. **Binary detection.** Any file containing a NUL byte, or that is not
   valid UTF-8, is copied byte-for-byte with no substitution. The whole
   file is scanned (not just a leading chunk), so a NUL past the first
   8192 bytes still marks the file as binary.
6. **Atomic replacement.** The whole result is built in a temporary
   directory created next to the output directory
   (`.envreplace-tmp-*`). Only after every file has been processed
   successfully is the old output removed and the temporary directory
   renamed into place. On any failure the temporary directory is removed,
   the existing output is left untouched, an error is printed to stderr,
   and the exit code is 1. An empty input tree mirrors to an empty output
   tree (stale files are removed). The output directory must not be the
   input directory, nor contain it, nor be contained in it. Because the
   old output is removed *before* the rename, a rename failure (for
   example, a vanished parent directory) can leave no output at all;
   there is no way to rename over a non-empty directory.
7. **Permissions.** File and directory permission bits are preserved
   from the input where practical.
8. **Symlinks.** A symbolic link to a file is followed and its target's
   content is written as a regular file at the corresponding output
   path. A symlink to a directory or to a special file is skipped with a
   log line (directory trees behind symlinks are not walked). A symlink
   whose target resolves *outside* the input directory fails the run:
   the input tree must be self-contained, and a link pointing outward
   would publish files the input did not ask for. Special files (FIFOs,
   sockets, devices) in the input are skipped with a log line.
9. **Logging.** Log lines go to stdout; errors go to stderr. Values are
   never printed — only file paths and replaced variable names.
   - `Processing: <in> -> <out>`
   - `  Replaced: NAME1, NAME2` (unique names, sorted) or
   - `  No replacements` or
   - `  Copied as binary (no replacements)`

### Exit codes

- `0` — success; the output tree is fully synchronized.
- non-zero — failure; the first error is reported, the previous output
  is untouched, and (for any run that reached the build phase) the
  temporary directory is cleaned up.

## Docker Compose example

```yaml
services:
  init:
    image: ghcr.io/MartinCa/docker-env-replace:latest
    env_file: .env
    environment:
      ENVREPLACE_INPUT_DIR: /input
      ENVREPLACE_OUTPUT_DIR: /output
    volumes:
      - ./templates:/input:ro
      - ./config:/output
    restart: "no"

  app:
    image: alpine:3.20
    depends_on:
      init:
        condition: service_completed_successfully
    volumes:
      - ./config:/config:ro
```

Mount the input templates **read-only** and the output directory
**writable by the image's non-root user** (uid/gid `65532`): the runtime
image is `gcr.io/distroless/static-debian12:nonroot`. On a typical host,
run `sudo chown -R 65532:65532 config` once so the container can write.

The full runnable composition is in [`docker-compose.yml`](docker-compose.yml)
with the example templates in [`examples/input`](examples/input/).

## Example templates

See [`examples/README.md`](examples/README.md):

- `examples/input/app.conf` — normal replacement (`<DB_HOST>`, `<DB_PORT>`).
- `examples/input/optional.conf` — explicit empty replacement using the
  `(empty)` sentinel, and the difference from unset/empty variables.

## Development

Prerequisites: the [Go](https://go.dev) toolchain (1.23 or newer).

```sh
go build ./...     # build the docker-env-replace binary
go test ./...      # run the test suite
gofmt -l .         # check formatting (run `go fmt ./...` to fix)
```

The Docker image is built multi-stage:

```dockerfile
FROM golang:1.23-alpine AS builder
# go build -o /out/docker-env-replace \
#     -ldflags='-linkmode=external -extldflags=-static'  -> static binary
FROM gcr.io/distroless/static-debian12:nonroot
ENTRYPOINT ["/usr/local/bin/docker-env-replace"]
```

The builder links a fully static, self-contained executable (the Go
runtime is statically embedded; there are no shared C library
dependencies), so the runtime stage is a shell-less distroless image
containing only that binary.

## Notes and assumptions

- **Distroless base image location.** `distroless/static-debian12` is
  published at `gcr.io/distroless/static-debian12:nonroot`; the Docker
  Hub `distroless` namespace no longer serves these images, so the
  Dockerfile references the GCR location.
- **Symlinks** are followed as described in rule 8; directory symlinks
  are not traversed.
- **Files are assumed small enough to read fully into memory** (they are
  configuration templates). The whole file is read, processed, and
  written.
- **Token names** may contain any bytes between the delimiters; an empty
  name (`<>` with the default delimiters) is not a token and is copied
  literally. A token delimiter that is never closed in a file is also
  copied literally.
- Values that themselves contain token syntax are **not** re-scanned:
  replacement is single-pass.