# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:21ade014faf952365b9ecbd8dbdb87176fbacb82fac9fb132d9f2a7b8cc3f132 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -buildvcs=false -trimpath -o /out/mobility-fixture .
RUN install -d -o 65532 -g 65532 -m 0700 /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/mobility-fixture /mobility-fixture
COPY --from=build --chown=65532:65532 --chmod=0700 /out/data /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/mobility-fixture"]
