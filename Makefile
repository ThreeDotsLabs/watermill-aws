include: .env

up:
	docker compose up -d

test:
	go test ./... -timeout=20m

test_v:
	go test -v ./...

test_short:
	go test ./... -short

test_race:
	go test ./... -short -race -timeout=30m

test_stress:
	STRESS_TEST_COUNT=3 go test -tags=stress -timeout=45m ./...

test_reconnect:
	go test -tags=reconnect ./...

test_codecov: up wait
	go test -coverprofile=coverage.out -covermode=atomic ./...

wait:
	go run github.com/ThreeDotsLabs/wait-for@latest localhost:4566

build:
	go build ./...

fmt:
	go fmt ./...
	goimports -l -w .

update_watermill:
	go get -u github.com/ThreeDotsLabs/watermill
	go mod tidy

	sed -i '\|go 1\.|d' go.mod
	go mod edit -fmt

