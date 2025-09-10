INDEXER_BINARY := indexer
KV_MIGRATE_BINARY := kv-migrate
WALLET_KV_LOAD_BINARY := wallet-kv-load

.PHONY: build build-all clean run stop run-ethereum-mainnet run-tron-mainnet wallet-kv-load

build:
	go build -o $(INDEXER_BINARY) ./cmd/indexer

build-kv-migrate:
	go build -o $(KV_MIGRATE_BINARY) ./cmd/kv-migrate

build-wallet-kv-load:
	go build -o $(WALLET_KV_LOAD_BINARY) ./cmd/wallet-kv-load

build-all: build build-kv-migrate build-wallet-kv-load

clean:
	rm -f $(INDEXER_BINARY) $(KV_MIGRATE_BINARY) $(WALLET_KV_LOAD_BINARY)

run:
	./$(INDEXER_BINARY) index --catchup

run-ethereum-mainnet: build
	./$(INDEXER_BINARY) index --chain=ethereum-mainnet --catchup

run-tron-mainnet: build
	./$(INDEXER_BINARY) index --chain=tron-mainnet --catchup

wallet-kv-load: build-wallet-kv-load
	./$(WALLET_KV_LOAD_BINARY)

stop:
	@echo "Killing indexer..."
	- pkill -f "./$(INDEXER_BINARY)" || true
