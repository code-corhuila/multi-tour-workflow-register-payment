# The register-payment saga as a container. Build context is the repository root.
FROM golang:1.26.9-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/workflow ./cmd/workflow

FROM alpine:3.22.6
# a user with no shell and no home: the process owns nothing it does not need
RUN adduser -D -H -s /sbin/nologin -u 10001 workflow
COPY --from=build /out/workflow /usr/local/bin/workflow
USER 10001
EXPOSE 8080
# busybox wget ships with alpine; /health needs no token. The port is the one of LISTEN_ADDR (":8080" when unset), so an
# environment that moves the listener (the gateway reaches the sagas on 8090) keeps a true health status.
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
  CMD sh -c 'a="${LISTEN_ADDR:-:8080}"; wget -q -O /dev/null "http://127.0.0.1:${a##*:}/health"' || exit 1
ENTRYPOINT ["/usr/local/bin/workflow"]
