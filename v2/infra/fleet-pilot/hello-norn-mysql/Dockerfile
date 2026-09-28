# syntax=docker/dockerfile:1.7
# The release workflow must supply reviewed digest-pinned base images.
ARG GO_IMAGE
ARG RUNTIME_IMAGE
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
ARG APP_VERSION
RUN test -n "$APP_VERSION" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/hello-norn-mysql .
FROM ${RUNTIME_IMAGE}
COPY --from=build /out/hello-norn-mysql /hello-norn-mysql
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hello-norn-mysql"]
