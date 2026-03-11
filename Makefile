IMAGE_NAME := xomrkob/transcoder-service
NAMESPACE := go-app
DEPLOYMENT := transcoder-service
VERSION ?= $(shell git describe --tags --always || echo "latest")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

.PHONY: all build push deploy clean

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
