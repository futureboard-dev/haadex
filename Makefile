BIN := haadex
INSTALL_DIR := $(HOME)/.local/bin

build:
	go build -o $(BIN) .

install: build
	mkdir -p $(INSTALL_DIR)
	ln -sf $(PWD)/$(BIN) $(INSTALL_DIR)/$(BIN)

uninstall:
	rm -f $(INSTALL_DIR)/$(BIN)

clean:
	rm -f $(BIN)

# Qdrant container lifecycle (.haadex/docker-compose.yml)
up:
	docker compose -f .haadex/docker-compose.yml up -d

down:
	docker compose -f .haadex/docker-compose.yml down

.PHONY: build install uninstall clean up down
