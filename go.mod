module github.com/status-im/status-go

go 1.12

require (
	github.com/NaySoftware/go-fcm v0.0.0-20190516140123-808e978ddcd2
	github.com/beevik/ntp v0.2.0
	github.com/btcsuite/btcd v0.0.0-20190523000118-16327141da8c
	github.com/btcsuite/btcutil v0.0.0-20190425235716-9e5f4b9a998d
	github.com/ethereum/go-ethereum v1.8.27
	github.com/go-playground/locales v0.12.1 // indirect
	github.com/go-playground/universal-translator v0.16.0 // indirect
	github.com/golang/mock v1.2.0
	github.com/leodido/go-urn v1.1.0 // indirect
	github.com/lib/pq v1.0.0
	github.com/libp2p/go-libp2p-core v0.0.3
	github.com/multiformats/go-multiaddr v0.0.4
	github.com/mutecomm/go-sqlcipher v0.0.0-20190227152316-55dbde17881f
	github.com/pborman/uuid v0.0.0-20170112150404-1b00554d8222
	github.com/russolsen/transit v0.0.0-20180705123435-0794b4c4505a
	github.com/status-im/doubleratchet v2.0.0+incompatible
	github.com/status-im/migrate/v4 v4.0.0-20190821140204-a9d340ec8fb76af4afda06acf01740d45d2661ed
	github.com/status-im/rendezvous v1.3.0
	github.com/status-im/status-protocol-go v0.0.0-20190701094942-8a6d5a6b49ee40f22ffac18377835e4a0fd7032e
	github.com/status-im/whisper v1.4.14
	github.com/stretchr/testify v1.3.1-0.20190712000136-221dbe5ed467
	github.com/syndtr/goleveldb v1.0.0
	go.uber.org/zap v1.10.0
	golang.org/x/crypto v0.0.0-20190701094942-4def268fd1a4
	golang.org/x/text v0.3.2
	gopkg.in/go-playground/assert.v1 v1.2.1 // indirect
	gopkg.in/go-playground/validator.v9 v9.29.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
)

replace github.com/ethereum/go-ethereum v1.8.27 => github.com/status-im/go-ethereum v1.8.27-status.5

replace github.com/NaySoftware/go-fcm => github.com/status-im/go-fcm v1.0.0-status
