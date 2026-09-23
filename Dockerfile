# Gate 3 Alpha image: single indexcore binary + explicit external rclone dependency.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.3.0-alpha
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X github.com/nathanxiangang-web/index-core/internal/runtime/version.Version=${VERSION} -X github.com/nathanxiangang-web/index-core/internal/runtime/version.Commit=${COMMIT} -X github.com/nathanxiangang-web/index-core/internal/runtime/version.Date=${DATE}" \
    -o /out/indexcore ./cmd/indexcore

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata curl unzip
# rclone is an explicit runtime dependency (external process, MIT), not linked in.
ARG RCLONE_VERSION=1.75.1
RUN curl -fsSL "https://downloads.rclone.org/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-linux-amd64.zip" -o /tmp/rclone.zip && \
    unzip -q /tmp/rclone.zip -d /tmp && \
    mv "/tmp/rclone-v${RCLONE_VERSION}-linux-amd64/rclone" /usr/local/bin/rclone && \
    chmod +x /usr/local/bin/rclone && \
    apk del curl unzip && rm -rf /tmp/*

COPY --from=build /out/indexcore /usr/local/bin/indexcore
USER nobody
EXPOSE 8080
ENTRYPOINT ["indexcore"]
CMD ["serve"]