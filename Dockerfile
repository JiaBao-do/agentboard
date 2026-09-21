# Optional: agentboard is a single static binary and does not need Docker.
#   docker build -t agentboard . && docker run -p 127.0.0.1:7878:7878 -v board-data:/data \
#     -e AGENTBOARD_TOKEN=change-me agentboard
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /agentboard ./cmd/agentboard

FROM scratch
COPY --from=build /agentboard /agentboard
USER 65532:65532
VOLUME /data
EXPOSE 7878
# Inside a container the port must listen on all interfaces, so a token is required (the server refuses to start without one).
ENTRYPOINT ["/agentboard", "serve", "-addr", "0.0.0.0:7878", "-data", "/data", "-allow-host", "localhost", "-allow-host", "127.0.0.1"]
