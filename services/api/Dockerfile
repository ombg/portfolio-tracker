# Build context is the repository root (see docker-compose.yml) so db/migrations can be copied.
FROM golang:1.27 AS build
WORKDIR /src
COPY services/api/go.mod services/api/go.sum ./
RUN go mod download
COPY services/api/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -o /out/ptctl ./cmd/ptctl

FROM gcr.io/distroless/static-debian12:nonroot
# Both on PATH, so `docker compose exec api ptctl user create ...` works.
COPY --from=build /out/api /out/ptctl /usr/local/bin/
COPY db/migrations /migrations
ENV MIGRATIONS_DIR=/migrations LISTEN_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["api"]
