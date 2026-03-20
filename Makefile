IMAGE_NAME := xomrkob/transcoder-service
NAMESPACE := go-app
DEPLOYMENT := transcoder-service
VERSION ?= $(shell git describe --tags --always || echo "latest")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
MODULE_NAME := github.com/mrhumster/transcoder-service

PROTO_DIR := proto/stream
GEN_DIR := gen/go

.PHONY: all build push deploy clean proto

all: build push deploy

build:
	@echo "Building docker image $(IMAGE_NAME):$(VERSION)..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE_NAME):$(VERSION) \
		-t $(IMAGE_NAME):latest .

push:
	@echo "Pushing image $(IMAGE_NAME):$(VERSION)..."
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest

deploy:
	@echo "Updating K8s deployment..."
	kubectl -n $(NAMESPACE) set image deployment/$(DEPLOYMENT) \
		transcoder-service=$(IMAGE_NAME):$(VERSION)
	@echo "Success!"

test:
	go test -v ./...

logs:
	kubectl -n $(NAMESPACE) logs -f -l app=transcoder

proto:
	@echo "Generate protoc"
	mkdir -p $(GEN_DIR)
	protoc --proto_path=$(PROTO_DIR) \
		--go_out=. --go-grpc_out=. \
		--go_opt=module=$(MODULE_NAME) \
		--go-grpc_opt=module=$(MODULE_NAME) \
		$(PROTO_DIR)/*.proto
	@echo "Proto file generated in $(GEN_DIR)"
