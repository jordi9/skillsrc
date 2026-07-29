MARKDOWN_FILES := $(shell find . -type f -name '*.md' \
	-not -path './.git/*' \
	-not -path './.jj/*' \
	-not -path './.now/*' | sort)

.PHONY: build install fmt fmt-check lint

build: fmt
	go build -o skillsrc ./cmd/skillsrc

install:
	go install ./cmd/skillsrc

fmt:
	go tool rewrap -w -c 120 $(MARKDOWN_FILES)

fmt-check:
	@status=0; for file in $(MARKDOWN_FILES); do \
		go tool rewrap -c 120 "$$file" | diff -u "$$file" - || status=1; \
	done; exit $$status

lint:
	golangci-lint run ./...
