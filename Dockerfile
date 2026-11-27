FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.4-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-builder /src/web/out ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tianfa-server ./cmd/server

FROM alpine:3.22
RUN addgroup -S tianfa && adduser -S -G tianfa tianfa \
    && mkdir -p /var/lib/tianfa /app/skills \
    && chown -R tianfa:tianfa /var/lib/tianfa /app
WORKDIR /app
COPY --from=go-builder /out/tianfa-server /usr/local/bin/tianfa-server
USER tianfa
EXPOSE 8080 8081
ENTRYPOINT ["tianfa-server"]


