module github.com/mauza/seekout/seekers/utahrealestate

go 1.22.1

require (
	github.com/go-redis/redis/v8 v8.11.5
	github.com/mauza/seekout v0.0.0-20240309032144-546ab2d4cb7e
	github.com/nats-io/nats.go v1.33.1
)

require (
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/guregu/null/v5 v5.0.0 // indirect
	github.com/klauspost/compress v1.17.2 // indirect
	github.com/nats-io/nkeys v0.4.7 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.18.0 // indirect
	golang.org/x/sys v0.16.0 // indirect
)

replace github.com/mauza/seekout => ../../
