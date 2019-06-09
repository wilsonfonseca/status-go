module github.com/status-im/status-go

go 1.12

require (
	github.com/NaySoftware/go-fcm v0.0.0-20190516140123-808e978ddcd2
	github.com/allegro/bigcache v1.1.0 // indirect
	github.com/beevik/ntp v0.2.0
	github.com/btcsuite/btcd v0.0.0-20190523000118-16327141da8c
	github.com/btcsuite/btcutil v0.0.0-20190425235716-9e5f4b9a998d
	github.com/cespare/cp v1.1.1 // indirect
	github.com/deckarep/golang-set v0.0.0-20180603214616-504e848d77ea // indirect
	github.com/elastic/gosigar v0.10.3 // indirect
	github.com/ethereum/go-ethereum v1.8.27
	github.com/fjl/memsize v0.0.0-20180427083637-f6d5545993d6 // indirect
	github.com/go-playground/locales v0.11.2 // indirect
	github.com/go-playground/universal-translator v0.16.0 // indirect
	github.com/golang-migrate/migrate v3.5.4+incompatible // indirect
	github.com/golang-migrate/migrate/v4 v4.3.1 // indirect
	github.com/golang/mock v1.2.0
	github.com/golang/protobuf v1.3.1
	github.com/influxdata/influxdb v1.7.6 // indirect
	github.com/karalabe/hid v0.0.0-20170821103837-f00545f9f374 // indirect
	github.com/lib/pq v1.0.0
	github.com/libp2p/go-libp2p-core v0.0.3
	github.com/mohae/deepcopy v0.0.0-20170929034955-c48cc78d4826 // indirect
	github.com/multiformats/go-multiaddr v0.0.4
	github.com/mutecomm/go-sqlcipher v0.0.0-20170920224653-f799951b4ab2
	github.com/pborman/uuid v0.0.0-20170112150404-1b00554d8222
	github.com/prometheus/prometheus v0.0.0-20170814170113-3101606756c5 // indirect
	github.com/rjeczalik/notify v0.9.2 // indirect
	github.com/rs/cors v0.0.0-20160617231935-a62a804a8a00 // indirect
	github.com/rs/xhandler v0.0.0-20160618193221-ed27b6fd6521 // indirect
	github.com/russolsen/ohyeah v0.0.0-20160324131710-f4938c005315 // indirect
	github.com/russolsen/same v0.0.0-20160222130632-f089df61f51d // indirect
	github.com/russolsen/transit v0.0.0-20180705123435-0794b4c4505a
	github.com/status-im/doubleratchet v2.0.0+incompatible
	github.com/status-im/migrate/v4 v4.3.1-status
	github.com/status-im/rendezvous v1.3.0
	github.com/status-im/whisper v1.4.13
	github.com/stretchr/objx v0.2.0 // indirect
	github.com/stretchr/testify v1.3.0
	github.com/syndtr/goleveldb v1.0.0
	golang.org/x/crypto v0.0.0-20190605123033-f99c8df09eb5
	golang.org/x/net v0.0.0-20190607181551-461777fb6f67 // indirect
	golang.org/x/sys v0.0.0-20190609082536-301114b31cce // indirect
	golang.org/x/text v0.3.2
	gopkg.in/go-playground/assert.v1 v1.2.1 // indirect
	gopkg.in/go-playground/validator.v9 v9.9.3
	gopkg.in/natefinch/lumberjack.v2 v2.0.0-20170531160350-a96e63847dc3
	gopkg.in/natefinch/npipe.v2 v2.0.0-20160621034901-c1b8fa8bdcce // indirect
	gopkg.in/olebedev/go-duktape.v3 v3.0.0-20180302121509-abf0ba0be5d5 // indirect
	gopkg.in/urfave/cli.v1 v1.20.0 // indirect
)

replace github.com/ethereum/go-ethereum v1.8.27 => github.com/status-im/go-ethereum v1.8.27-status.2
