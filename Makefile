.PHONY: init up down deps generate test integration api worker agent fmt

init:
	python3 scripts/init_env.py

up: init
	docker compose up -d --build

down:
	docker compose down

deps:
	go mod download
	python3 -m venv ai-agent/.venv
	ai-agent/.venv/bin/pip install -r ai-agent/requirements.txt

generate:
	GOBIN="$(CURDIR)/work/bin" go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
	GOBIN="$(CURDIR)/work/bin" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
	PATH="$(CURDIR)/work/bin:$$PATH" protoc -I proto --go_out=. --go_opt=module=protobuf --go-grpc_out=. --go-grpc_opt=module=protobuf proto/agent.proto
	ai-agent/.venv/bin/python -m grpc_tools.protoc -I proto --python_out=ai-agent/generated --grpc_python_out=ai-agent/generated proto/agent.proto

fmt:
	gofmt -w cmd internal migrations/migrations.go web/assets.go

test:
	go test ./...
	ai-agent/.venv/bin/python -m unittest discover -s ai-agent/tests -v

integration: init
	docker compose up -d --wait postgres redis
	python3 scripts/integration.py

api:
	go run ./cmd/api
worker:
	go run ./cmd/worker
agent:
	ai-agent/.venv/bin/python ai-agent/server.py
