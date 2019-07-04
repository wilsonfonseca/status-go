package main

import (
	"flag"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/status-im/status-go/mailserver"
	whisper "github.com/status-im/whisper/whisperv6"

	"github.com/status-im/status-go/params"
)

// WMailServer whisper mailserver.
type Migrate struct {
	pg    mailserver.DB
	level mailserver.DB
}

func (s *Migrate) Init(config *params.WhisperConfig) error {

	// Open database in the last step in order not to init with error
	// and leave the database open by accident.
	pg, err := mailserver.NewPostgresDB(config)
	if err != nil {
		return fmt.Errorf("open DB: %s", err)
	}
	s.pg = pg

	level, err := mailserver.NewLevelDB(config)
	if err != nil {
		return fmt.Errorf("open DB: %s", err)
	}
	s.level = level
	return nil
}

func (s *Migrate) createIterator(lower, upper uint32) (mailserver.Iterator, error) {
	var (
		emptyHash  common.Hash
		emptyTopic whisper.TopicType
		ku, kl     *mailserver.DBKey
	)

	ku = mailserver.NewDBKey(upper+1, emptyTopic, emptyHash)
	kl = mailserver.NewDBKey(lower, emptyTopic, emptyHash)

	query := mailserver.NewSimpleCursorQuery(kl.Bytes(), ku.Bytes())
	return s.level.BuildIterator(query)
}

func (s *Migrate) Migrate(start int) error {
	fmt.Println("Syncing from:", start)
	bloom := whisper.MakeFullNodeBloom()
	iter, err := s.createIterator(uint32(start), 2500000000)
	if err != nil {
		return err
	}
	fmt.Println("GOT ITERATOR")
	defer iter.Release()
	for iter.Next() {

		rawValue, err := iter.GetEnvelope(bloom)
		var envelope whisper.Envelope

		if err != nil {
			return err

		}
		decodeErr := rlp.DecodeBytes(rawValue, &envelope)
		if decodeErr != nil {
			return err
		}

		err = s.pg.SaveEnvelope(&envelope)
		if err != nil {
			return err
		}

	}

	return nil
}

func main() {
	start := flag.Int("start", 0, "unix timestamp to start syncing")
	dataDir := flag.String("datadir", "/docker/statusd-mail/data/wnode", "the dir to use")
	pgURI := flag.String("pguri", "postgres://test:test@localhost:5432/whisper?sslmode=disable", "the pg uri to use")
	flag.Parse()

	config := &params.WhisperConfig{
		DataDir: *dataDir,
		DatabaseConfig: params.DatabaseConfig{
			PGConfig: params.PGConfig{
				Enabled: true,
				URI:     *pgURI,
			},
		},
	}

	migrate := &Migrate{}
	err := migrate.Init(config)
	if err != nil {
		fmt.Println("ERROR", err)
	}
	err = migrate.Migrate(*start)
	if err != nil {
		fmt.Println("MIGRATING ERROR", err)
	}

}
