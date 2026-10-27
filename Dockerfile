# syntax=docker/dockerfile:1

# ---- build: compiles the server ---------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- test: `docker compose run --rm tests` ----------------------------------
# Reuses the build stage (Go toolchain + gcc, needed by -race). Integration
# tests run against the Redis pointed to by REDIS_ADDR.
FROM build AS test
CMD ["go", "test", "-race", "-count=1", "./..."]

# ---- runtime: minimal, non-root image (default target) ----------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/server /server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
