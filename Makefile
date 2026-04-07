.PHONY: check-deps build clean kill renew init

BUILD_DIR := build
BINARY := $(BUILD_DIR)/watcher
GO_MIN_VERSION := 1.21

check-deps:
	@echo "Checking dependencies..."
	@command -v go >/dev/null 2>&1 || { echo "ERROR: go is not installed. Install Go >= $(GO_MIN_VERSION) from https://go.dev/dl/"; exit 1; }
	@GO_VERSION=$$(go version | grep -oE '[0-9]+\.[0-9]+' | head -1); \
	GO_MAJOR=$$(echo $$GO_VERSION | cut -d. -f1); \
	GO_MINOR=$$(echo $$GO_VERSION | cut -d. -f2); \
	REQ_MAJOR=$$(echo $(GO_MIN_VERSION) | cut -d. -f1); \
	REQ_MINOR=$$(echo $(GO_MIN_VERSION) | cut -d. -f2); \
	if [ "$$GO_MAJOR" -lt "$$REQ_MAJOR" ] || { [ "$$GO_MAJOR" -eq "$$REQ_MAJOR" ] && [ "$$GO_MINOR" -lt "$$REQ_MINOR" ]; }; then \
		echo "ERROR: Go >= $(GO_MIN_VERSION) required, found $$GO_VERSION"; exit 1; \
	fi; \
	echo "  go $$GO_VERSION OK"
	@command -v gcc >/dev/null 2>&1 || command -v clang >/dev/null 2>&1 || { echo "ERROR: gcc or clang is required (CGo dependency for SQLite). Install Xcode Command Line Tools: xcode-select --install"; exit 1; }
	@CC=$$(command -v gcc 2>/dev/null || command -v clang 2>/dev/null); \
	echo "  C compiler: $$CC OK"
	@echo "All dependencies satisfied."

build: check-deps
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 go build -o $(BINARY) .

clean:
	rm -rf $(BUILD_DIR)

kill:
	@pgrep -f '[w]atcher .*serve' > /dev/null 2>&1 \
		&& pgrep -f '[w]atcher .*serve' | xargs kill && echo "watcher stopped" \
		|| echo "watcher is not running"

renew: init kill
	@echo "Starting $(BINARY)..."
	@nohup ./$(BINARY) serve > /dev/null 2>&1 & echo "watcher started (PID $$!)"

init: build
	@./$(BINARY) init
