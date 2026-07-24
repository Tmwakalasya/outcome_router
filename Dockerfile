FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outcome-router ./cmd/router \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-provider ./cmd/mock-provider

FROM alpine:3.22

RUN addgroup -S router && adduser -S -G router router
WORKDIR /app
COPY --from=builder /out/outcome-router /usr/local/bin/outcome-router
COPY --from=builder /out/mock-provider /usr/local/bin/mock-provider
COPY config ./config
RUN mkdir -p /var/lib/outcome-router && chown -R router:router /var/lib/outcome-router
USER router
EXPOSE 8080
ENV OUTCOME_ROUTER_CONFIG=/app/config/demo.json
CMD ["outcome-router"]
