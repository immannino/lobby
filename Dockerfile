# The lobby website (examples/site): landing page, docs and live demo.
# Builds a static binary; no cgo needed (SQLite is pure Go).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/site ./examples/site

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/site /site
# Mount a volume at /data so rooms survive deploys. Without one, rooms are
# kept for the life of the container.
ENV LOBBY_DB=/data/lobby.db \
    LOBBY_LOG_JSON=true \
    PORT=8080
EXPOSE 8080
ENTRYPOINT ["/site"]
