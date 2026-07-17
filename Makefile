.PHONY: build test e2e fuzz bench web run clean

build: web
	go build -o bin/origoad ./cmd/origoad

test:
	go vet ./...
	test -z "$$(gofmt -l .)"
	go test -race ./...

# Run the same suite against PostgreSQL:
#   ORIGOA_TEST_DSN=postgres://... make test

e2e: build
	rm -rf /tmp/origoa-e2e.git; \
	./bin/origoad -repo /tmp/origoa-e2e.git -addr 127.0.0.1:18099 & pid=$$!; \
	sleep 1; ./scripts/e2e.sh http://127.0.0.1:18099; status=$$?; \
	kill $$pid 2>/dev/null; rm -rf /tmp/origoa-e2e.git; exit $$status

fuzz:
	go test -run xxx -fuzz FuzzRoundTrip -fuzztime 30s ./internal/ojson/
	go test -run xxx -fuzz FuzzCleanFolder -fuzztime 30s ./internal/core/
	go test -run xxx -fuzz FuzzClassify -fuzztime 30s ./internal/core/

bench:
	go test -run xxx -bench . -benchtime 50x ./internal/core/

web:
	cd web && npm install --no-audit --no-fund && npx tsc --noEmit && npm run build

run: build
	./bin/origoad -repo data/origoa.git -web web/dist

clean:
	rm -rf bin web/dist web/node_modules
