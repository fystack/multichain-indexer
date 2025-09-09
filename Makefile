BINARY := indexer
PKG := ./cmd/indexer

build:
	go build -o $(BINARY) $(PKG)

run:
	./$(BINARY) index --catchup

run-ethereum-mainnet: build
	./$(BINARY) index --chain=ethereum-mainnet --catchup

run-tron-mainnet: build
	./$(BINARY) index --chain=tron-mainnet --catchup

stop:
	@echo "Killing indexer..."
	- pkill -f "./$(BINARY)" || true
