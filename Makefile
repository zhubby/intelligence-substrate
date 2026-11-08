.PHONY: build test check run web-check browser-test blueprint-test

build:
	rtk proxy npm --prefix web ci
	rtk proxy npm --prefix web run build
	rtk proxy go build -trimpath -o bin/is-platform ./cmd/platform
	rtk proxy go build -trimpath -o bin/is-worker ./cmd/worker
	rtk proxy go build -trimpath -o bin/is-admin ./cmd/admin
	rtk proxy go build -trimpath -o bin/is-publisher ./cmd/publisher
	rtk proxy go build -trimpath -o bin/is-distribution ./cmd/distribution
	rtk proxy go build -trimpath -o bin/is-runtime ./cmd/runtime
	rtk proxy go build -trimpath -o bin/is-updater ./cmd/updater
	rtk proxy go build -trimpath -o bin/is-signer-archive ./cmd/signer-archive

test:
	rtk proxy go test ./...

check:
	rtk proxy go vet ./...
	rtk proxy go test -race -timeout=20m ./...
	rtk proxy npm --prefix web run check
	rtk proxy npm --prefix web run build

run:
	rtk proxy env IS_DEV_MODE=true IS_BLUEPRINT_ROOT=. go run ./cmd/platform

browser-test:
	rtk proxy npm --prefix web run test:e2e

blueprint-test:
	rtk proxy python3 -m unittest discover -s scripts -p 'test_*.py' -v
	rtk proxy go -C blueprints/flutter-go/backend test ./...
