# Classy Cash — minimal container (pure Go, CGO-free, SQLite embedded).
# Build from the repository root:
#   docker build -f deploy/Dockerfile --build-arg VERSION=$(date -u +%Y%m%d%H%M) -t classycash:dev .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/classy-cash/app/internal/version.Version=${VERSION} -X github.com/classy-cash/app/internal/version.Commit=${COMMIT} -X github.com/classy-cash/app/internal/version.Date=${DATE}" \
    -o /out/classycash ./cmd/classycash

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 app && mkdir -p /data && chown app:app /data
COPY --from=build /out/classycash /usr/local/bin/classycash
USER app
ENV CLASSYCASH_ADDR=0.0.0.0:8080 \
    CLASSYCASH_DB_PATH=/data/classycash.db \
    CLASSYCASH_DATA_DIR=/data \
    CLASSYCASH_TRUST_PROXY=1
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD wget -q --spider http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["classycash"]
CMD ["serve"]
