# syntax=docker/dockerfile:1.7
ARG GO_IMAGE
ARG RUNTIME_IMAGE
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
ARG APP_VERSION
RUN test -n "$APP_VERSION" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/norn-fleet-canary .
FROM ${RUNTIME_IMAGE}
COPY --from=build /out/norn-fleet-canary /norn-fleet-canary
USER 65532:65532
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/norn-fleet-canary"]
