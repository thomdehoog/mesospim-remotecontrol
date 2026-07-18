# syntax=docker/dockerfile:1
#
# Multi-stage build for the Origoa Foundation: the frontend is built to static
# assets, the backend to a static binary that serves both the REST/WebSocket
# API and those assets, and the runtime image carries only the binaries plus
# the built SPA. See SETUP.md for how to run it with docker compose.

# --- 1. Build the frontend ---
FROM node:22-slim AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- 2. Build the backend (static, no cgo) ---
FROM golang:1.25 AS backend
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/origoad ./cmd/origoad \
 && CGO_ENABLED=0 go build -trimpath -o /out/origoa-seed ./cmd/origoa-seed

# --- 3. Runtime ---
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=backend  /out/origoad /out/origoa-seed /usr/local/bin/
COPY --from=frontend /app/frontend/dist /app/static
ENV ORIGOA_STATIC=/app/static \
    ORIGOA_GIT_DIR=/data/repo.git \
    ORIGOA_LISTEN=:8000
EXPOSE 8000
# CMD (not ENTRYPOINT) so `docker compose run backend origoa-seed` can override it.
CMD ["origoad"]
