FROM node:20-alpine AS ui-build
RUN corepack enable && corepack prepare pnpm@latest --activate
WORKDIR /app/ui
COPY ui/package.json ui/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile || pnpm install
COPY ui/ .
RUN pnpm build

FROM golang:1.25-alpine AS api-build
WORKDIR /app
COPY api/go.mod api/go.sum ./
RUN go mod download
COPY api/ .
RUN CGO_ENABLED=0 go build -o /norn .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=api-build /norn /usr/local/bin/norn
COPY --from=ui-build /app/ui/dist /srv/ui
ENV NORN_UI_DIR=/srv/ui
ENV NORN_PORT=8800
EXPOSE 8800
CMD ["norn"]
