package dedup

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

type dummyKeyPairProvider struct {
	id string
}

func (p dummyKeyPairProvider) SelectedKeyPairID() string {
	return p.id
}

func BenchmarkDeduplicate30000MessagesADay(b *testing.B) {
	// using on-disk db here for real benchmarks
	dir, err := ioutil.TempDir("", "dedup-30000")
	if err != nil {
		panic(err)
	}

	defer func() {
		err := os.RemoveAll(dir)
		if err != nil {
			panic(err)
		}
	}()

	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		panic(err)
	}

	d := NewDeduplicator(dummyKeyPairProvider{}, db)

	b.Log("generating messages")
	messagesOld := generateDedupMessages(100000)
	b.Log("generation is done")

	// pre-fill deduplicator
	d.Deduplicate(messagesOld[:1000])

	b.ResetTimer()
	length := 300
	start := 1000
	for n := 0; n < b.N; n++ {
		if n%100 == 0 {
			d.cache.now = func() time.Time { return time.Now().Add(time.Duration(24*(n/100)) * time.Hour) }
		}
		if (start + length) >= len(messagesOld) {
			start = 0
			fmt.Println("cycle!")
		}
		messages := messagesOld[start:(start + length)]
		start += length
		dedupMessages := d.Deduplicate(messages)
		ids := make([][]byte, len(dedupMessages))
		for i, m := range dedupMessages {
			ids[i] = m.Metadata.DedupID
		}
		assert.NoError(b, d.AddMessagesByID(ids))
	}
}

func TestDeduplicatorTestSuite(t *testing.T) {
	suite.Run(t, new(DeduplicatorTestSuite))
}

type DeduplicatorTestSuite struct {
	suite.Suite
	d  *Deduplicator
	db *leveldb.DB
}

func (s *DeduplicatorTestSuite) SetupTest() {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		panic(err)
	}
	s.db = db
	s.d = NewDeduplicator(dummyKeyPairProvider{}, db)
}

func (s *DeduplicatorTestSuite) TearDownTest() {
	s.NoError(s.db.Close())
}

func (s *DeduplicatorTestSuite) TestDeduplicateSingleFilter() {
	s.d.keyPairProvider = dummyKeyPairProvider{"acc1"}
	messages1 := generateDedupMessages(10)
	messages2 := generateDedupMessages(12)

	result := s.d.Deduplicate(messages1)
	s.Equal(len(messages1), len(result))

	ids := make([][]byte, len(result))
	for i, m := range result {
		ids[i] = m.Metadata.DedupID
	}
	s.NoError(s.d.AddMessagesByID(ids))

	result = s.d.Deduplicate(messages1)
	s.Equal(0, len(result))

	result = s.d.Deduplicate(messages2)
	s.Equal(len(messages2), len(result))

	ids = make([][]byte, len(result))
	for i, m := range result {
		ids[i] = m.Metadata.DedupID
	}
	s.NoError(s.d.AddMessagesByID(ids))

	messages3 := append(messages2, generateDedupMessages(11)...)

	result = s.d.Deduplicate(messages3)
	s.Equal(11, len(result))
}

func (s *DeduplicatorTestSuite) TestDeduplicateMultipleFilters() {
	messages1 := generateDedupMessages(10)

	s.d.keyPairProvider = dummyKeyPairProvider{"acc1"}
	result := s.d.Deduplicate(messages1)
	s.Equal(len(messages1), len(result))
	ids := make([][]byte, len(result))
	for i, m := range result {
		ids[i] = m.Metadata.DedupID
	}

	s.NoError(s.d.AddMessagesByID(ids))

	result = s.d.Deduplicate(messages1)
	s.Equal(0, len(result))

	s.d.keyPairProvider = dummyKeyPairProvider{"acc2"}
	result = s.d.Deduplicate(messages1)
	s.Equal(len(messages1), len(result))
}
