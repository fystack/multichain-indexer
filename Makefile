BINARY := indexer
PKG := ./cmd/indexer

build:
	go build -o $(BINARY) $(PKG)

run-evm: build
	./$(BINARY) index --chain=evm --debug

run-tron: build
	./$(BINARY) index --chain=tron --debug

stop:
	@echo "Killing indexer..."
	- pkill -f "./$(BINARY)" || true
