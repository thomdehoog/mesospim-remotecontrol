# Origoa Foundation — common tasks. Run `make` for the list.
# Paths are relative to this directory (origoa/).

BACKEND      := backend
FRONTEND     := frontend
DSN          ?= postgres://origoa:origoa@localhost:5432/origoa?sslmode=disable
TEST_DSN     ?= postgres://origoa:origoa@localhost:5432/origoa_test
GIT_DIR      ?= $(CURDIR)/data/repo.git
STATIC       := $(CURDIR)/$(FRONTEND)/dist

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: setup
setup: ## Install frontend dependencies
	cd $(FRONTEND) && npm install

.PHONY: build
build: build-frontend build-backend ## Build the frontend and backend

.PHONY: build-frontend
build-frontend: ## Build the SPA to frontend/dist
	cd $(FRONTEND) && npm run build

.PHONY: build-backend
build-backend: ## Build the origoad and origoa-seed binaries
	cd $(BACKEND) && go build -o ../bin/origoad ./cmd/origoad
	cd $(BACKEND) && go build -o ../bin/origoa-seed ./cmd/origoa-seed

.PHONY: test
test: test-backend test-frontend-unit ## Run backend tests and frontend unit tests

.PHONY: test-backend
test-backend: ## Backend unit + integration + torture/fuzz (serialized; shares one test DB)
	cd $(BACKEND) && ORIGOA_TEST_DSN="$(TEST_DSN)" go test -p 1 ./...

.PHONY: test-frontend-unit
test-frontend-unit: ## Frontend unit tests (vitest + jsdom)
	cd $(FRONTEND) && npm run test:unit

.PHONY: seed
seed: ## Seed the demo dataset into an empty repository
	cd $(BACKEND) && ORIGOA_DSN="$(DSN)" ORIGOA_GIT_DIR="$(GIT_DIR)" go run ./cmd/origoa-seed

.PHONY: run
run: build-frontend ## Run the server (serves the built SPA at http://localhost:8000)
	cd $(BACKEND) && ORIGOA_DSN="$(DSN)" ORIGOA_GIT_DIR="$(GIT_DIR)" ORIGOA_STATIC="$(STATIC)" go run ./cmd/origoad

.PHONY: dev
dev: ## Run the Vite dev server with hot reload (proxies the API to :8000)
	cd $(FRONTEND) && npm run dev

.PHONY: docker-up
docker-up: ## Build and start the full stack with docker compose
	docker compose up -d --build

.PHONY: docker-seed
docker-seed: ## Seed the demo dataset inside the compose stack
	docker compose run --rm backend origoa-seed
	docker compose restart backend

.PHONY: docker-down
docker-down: ## Stop the compose stack (keeps volumes)
	docker compose down

.PHONY: clean
clean: ## Remove build outputs and local repository data
	rm -rf bin $(FRONTEND)/dist data
