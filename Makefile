run:
	go run ./cmd/app

test:
	go test ./...

lint:
	go vet ./...

build:
	go build -o bin/app ./cmd/app
