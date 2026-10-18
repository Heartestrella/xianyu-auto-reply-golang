FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/xianyu-go ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=backend /out/xianyu-go ./xianyu-go
COPY --from=frontend /src/frontend/dist ./frontend/dist
RUN mkdir -p /app/data && chown -R app:app /app
USER app
ENV TZ=Asia/Shanghai LISTEN_ADDR=:8089 DATA_DIR=/app/data STATIC_DIR=/app/frontend/dist
EXPOSE 8089
VOLUME ["/app/data"]
ENTRYPOINT ["/app/xianyu-go"]
