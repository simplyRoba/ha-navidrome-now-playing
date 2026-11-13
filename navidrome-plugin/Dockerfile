FROM golang:1.25 AS build
WORKDIR /src
COPY navidrome-plugin/go.mod navidrome-plugin/go.sum ./
RUN go mod download
COPY navidrome-plugin/*.go navidrome-plugin/manifest.json ./
COPY navidrome-plugin/cmd/ ./cmd/
RUN mkdir -p /out && \
    GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -o /out/plugin.wasm . && \
    go run ./cmd/package manifest.json /out/plugin.wasm /out/navidrome-nowplaying.ndp

FROM scratch
COPY --from=build /out/navidrome-nowplaying.ndp /navidrome-nowplaying.ndp
