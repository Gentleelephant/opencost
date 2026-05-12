GO ?= go
SHELL := bash
IMAGE_TAG ?= $(shell ./tools/image-tag)
GIT_REVISION := $(shell git rev-parse --short HEAD)
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_LAST_COMMIT_DATE := $(shell git log -1 --date=iso-strict --format=%cd)

# Build flags
VPREFIX := github.com/opencost/opencost/core/pkg/version
GO_LDFLAGS   := -X $(VPREFIX).Version=$(IMAGE_TAG) -X $(VPREFIX).GitCommit=$(GIT_REVISION)
GO_FLAGS     := -ldflags "-extldflags \"-static\" -s -w $(GO_LDFLAGS)"

.PHONY: go/bin
go/bin:
	CGO_ENABLED=0 $(GO) build $(GO_FLAGS) ./cmd/costmodel

.PHONY: swagger-verify
swagger-verify:
	env -u GOROOT $(GO) run ./tools/swaggercheck -mode static

.PHONY: swagger-smoke-minimal
swagger-smoke-minimal:
	env -u GOROOT $(GO) run ./tools/swaggercheck -mode smoke -profile minimal-local -start-local

.PHONY: swagger-params
swagger-params:
	env -u GOROOT $(GO) run ./tools/swaggercheck -mode params

.PHONY: swagger-generate
swagger-generate:
	env -u GOROOT swag init -g cmd/costmodel/main.go -o docs
