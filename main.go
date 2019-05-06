// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"

	_ "github.com/lib/pq"

	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/status-im/status-go/db"
	whisper "github.com/status-im/whisper/whisperv6"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	maxQueryRange = 24 * time.Hour
	defaultLimit  = 2000
	// When we default the upper limit, we want to extend the range a bit
	// to accommodate for envelopes with slightly higher timestamp, in seconds
	whisperTTLSafeThreshold = 60
)

var (
	errDirectoryNotProvided        = errors.New("data directory not provided")
	errDecryptionMethodNotProvided = errors.New("decryption method is not provided")
)

const (
	timestampLength        = 4
	requestLimitLength     = 4
	requestTimeRangeLength = timestampLength * 2
	processRequestTimeout  = time.Minute
)

// dbImpl is an interface introduced to be able to test some unexpected
// panics from leveldb that are difficult to reproduce.
// normally the db implementation is leveldb.DB, but in TestMailServerDBPanicSuite
// we use panicDB to test panics from the db.
// more info about the panic errors:
// https://github.com/syndtr/goleveldb/issues/224
type dbImpl interface {
	Close() error
	Write(*leveldb.Batch, *opt.WriteOptions) error
	Put([]byte, []byte, *opt.WriteOptions) error
	Get([]byte, *opt.ReadOptions) ([]byte, error)
	NewIterator(*util.Range, *opt.ReadOptions) iterator.Iterator
}

// WMailServer whisper mailserver.
type WMailServer struct {
	db         dbImpl
	w          *whisper.Whisper
	pow        float64
	symFilter  *whisper.Filter
	asymFilter *whisper.Filter

	muRateLimiter sync.RWMutex
}

// Init initializes mailServer.
func (s *WMailServer) Init() error {
	var err error

	// Open database in the last step in order not to init with error
	// and leave the database open by accident.
	database, err := db.Open("../wnode", nil)
	if err != nil {
		return fmt.Errorf("open DB: %s", err)
	}
	s.db = database

	return nil
}

// Close the mailserver and its associated db connection.
func (s *WMailServer) Close() {
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			log.Error(fmt.Sprintf("s.db.Close failed: %s", err))
		}
	}
}

func recoverLevelDBPanics(calleMethodName string) {
	// Recover from possible goleveldb panics
	if r := recover(); r != nil {
		if errString, ok := r.(string); ok {
			log.Error(fmt.Sprintf("recovered from panic in %s: %s", calleMethodName, errString))
		}
	}
}

func (s *WMailServer) createIterator(lower, upper uint32, cursor []byte) iterator.Iterator {
	var (
		emptyHash common.Hash
		ku, kl    *DBKey
	)

	kl = NewDBKey(lower, emptyHash)
	if len(cursor) == DBKeyLength {
		ku = mustNewDBKeyFromBytes(cursor)
	} else {
		ku = NewDBKey(upper+1, emptyHash)
	}

	i := s.db.NewIterator(&util.Range{Start: kl.Bytes(), Limit: ku.Bytes()}, nil)
	// seek to the end as we want to return envelopes in a descending order
	i.Seek(ku.Bytes())

	return i
}

// processRequestInBundles processes envelopes using an iterator and passes them
// to the output channel in bundles.
func (s *WMailServer) processRequestInBundles(
	iter iterator.Iterator,
	bloom []byte,
	limit int,
	timeout time.Duration,
	requestID string,
	output chan<- []*whisper.Envelope,
	cancel <-chan struct{},
) ([]byte, common.Hash) {
	var (
		bundle                 []*whisper.Envelope
		bundleSize             uint32
		batches                [][]*whisper.Envelope
		processedEnvelopes     int
		processedEnvelopesSize int64
		nextCursor             []byte
		lastEnvelopeHash       common.Hash
	)

	log.Info("[mailserver:processRequestInBundles] processing request",
		"requestID", requestID,
		"limit", limit)

	// We iterate over the envelopes.
	// We collect envelopes in batches.
	// If there still room and we haven't reached the limit
	// append and continue.
	// Otherwise publish what you have so far, reset the bundle to the
	// current envelope, and leave if we hit the limit
	for iter.Prev() {
		var envelope whisper.Envelope

		decodeErr := rlp.DecodeBytes(iter.Value(), &envelope)
		if decodeErr != nil {
			log.Error("[mailserver:processRequestInBundles] failed to decode RLP",
				"err", decodeErr,
				"requestID", requestID)
			continue
		}

		if !whisper.BloomFilterMatch(bloom, envelope.Bloom()) {
			continue
		}

		lastEnvelopeHash = envelope.Hash()
		processedEnvelopes++
		envelopeSize := whisper.EnvelopeHeaderLength + uint32(len(envelope.Data))
		limitReached := processedEnvelopes == limit
		newSize := bundleSize + envelopeSize

		// If we still have some room for messages, add and continue
		if !limitReached && newSize < s.w.MaxMessageSize() {
			bundle = append(bundle, &envelope)
			bundleSize = newSize
			continue
		}

		// Publish if anything is in the bundle (there should always be
		// something unless limit = 1)
		if len(bundle) != 0 {
			batches = append(batches, bundle)
			processedEnvelopesSize += int64(bundleSize)
		}

		// Reset the bundle with the current envelope
		bundle = []*whisper.Envelope{&envelope}
		bundleSize = envelopeSize

		// Leave if we reached the limit
		if limitReached {
			nextCursor = iter.Key()
			break
		}
	}

	if len(bundle) > 0 {
		batches = append(batches, bundle)
		processedEnvelopesSize += int64(bundleSize)
	}

	log.Info("[mailserver:processRequestInBundles] publishing envelopes",
		"requestID", requestID,
		"batchesCount", len(batches),
		"envelopeCount", processedEnvelopes,
		"processedEnvelopesSize", processedEnvelopesSize,
		"cursor", nextCursor)

	// Publish
	for _, batch := range batches {
		select {
		case output <- batch:
		// It might happen that during producing the batches,
		// the connection with the peer goes down and
		// the consumer of `output` channel exits prematurely.
		// In such a case, we should stop pushing batches and exit.
		case <-cancel:
			log.Info("[mailserver:processRequestInBundles] failed to push all batches",
				"requestID", requestID)
			break
		case <-time.After(timeout):
			log.Error("[mailserver:processRequestInBundles] timed out pushing a batch",
				"requestID", requestID)
			break
		}
	}

	log.Info("[mailserver:processRequestInBundles] envelopes published",
		"requestID", requestID)
	close(output)

	return nextCursor, lastEnvelopeHash
}

func (s *WMailServer) sendEnvelopes(peer *whisper.Peer, envelopes []*whisper.Envelope, batch bool) error {
	if batch {
		return s.w.SendP2PDirect(peer, envelopes...)
	}

	for _, env := range envelopes {
		if err := s.w.SendP2PDirect(peer, env); err != nil {
			return err
		}
	}

	return nil
}

func (s *WMailServer) sendHistoricMessageResponse(peer *whisper.Peer, request *whisper.Envelope, lastEnvelopeHash common.Hash, cursor []byte) error {
	payload := whisper.CreateMailServerRequestCompletedPayload(request.Hash(), lastEnvelopeHash, cursor)
	return s.w.SendHistoricMessageResponse(peer, payload)
}

// this method doesn't return an error because it is already in the error handling chain
func (s *WMailServer) trySendHistoricMessageErrorResponse(peer *whisper.Peer, request *whisper.Envelope, errorToReport error) {
	payload := whisper.CreateMailServerRequestFailedPayload(request.Hash(), errorToReport)

	err := s.w.SendHistoricMessageResponse(peer, payload)
	// if we can't report an error, probably something is wrong with p2p connection,
	// so we just print a log entry to document this sad fact
	if err != nil {
		log.Error("Error while reporting error response", "err", err, "peerID", peerIDString(peer))
	}
}

// openEnvelope tries to decrypt an envelope, first based on asymetric key (if
// provided) and second on the symetric key (if provided)
func (s *WMailServer) openEnvelope(request *whisper.Envelope) *whisper.ReceivedMessage {
	if s.asymFilter != nil {
		if d := request.Open(s.asymFilter); d != nil {
			return d
		}
	}
	if s.symFilter != nil {
		if d := request.Open(s.symFilter); d != nil {
			return d
		}
	}
	return nil
}

// validateRequest runs different validations on the current request.
// DEPRECATED
func (s *WMailServer) validateRequest(
	peerID []byte,
	request *whisper.Envelope,
) (uint32, uint32, []byte, uint32, []byte, error) {
	if s.pow > 0.0 && request.PoW() < s.pow {
		return 0, 0, nil, 0, nil, fmt.Errorf("PoW() is too low")
	}

	decrypted := s.openEnvelope(request)
	if decrypted == nil {
		return 0, 0, nil, 0, nil, fmt.Errorf("failed to decrypt p2p request")
	}

	if err := s.checkMsgSignature(decrypted, peerID); err != nil {
		return 0, 0, nil, 0, nil, err
	}

	bloom, err := s.bloomFromReceivedMessage(decrypted)
	if err != nil {
		return 0, 0, nil, 0, nil, err
	}

	lower := binary.BigEndian.Uint32(decrypted.Payload[:4])
	upper := binary.BigEndian.Uint32(decrypted.Payload[4:8])

	if upper < lower {
		err := fmt.Errorf("query range is invalid: from > to (%d > %d)", lower, upper)
		return 0, 0, nil, 0, nil, err
	}

	lowerTime := time.Unix(int64(lower), 0)
	upperTime := time.Unix(int64(upper), 0)
	if upperTime.Sub(lowerTime) > maxQueryRange {
		err := fmt.Errorf("query range too big for peer %s", string(peerID))
		return 0, 0, nil, 0, nil, err
	}

	var limit uint32
	if len(decrypted.Payload) >= requestTimeRangeLength+whisper.BloomFilterSize+requestLimitLength {
		limit = binary.BigEndian.Uint32(decrypted.Payload[requestTimeRangeLength+whisper.BloomFilterSize:])
	}

	var cursor []byte
	if len(decrypted.Payload) == requestTimeRangeLength+whisper.BloomFilterSize+requestLimitLength+DBKeyLength {
		cursor = decrypted.Payload[requestTimeRangeLength+whisper.BloomFilterSize+requestLimitLength:]
	}

	err = nil
	return lower, upper, bloom, limit, cursor, err
}

// checkMsgSignature returns an error in case the message is not correcly signed
func (s *WMailServer) checkMsgSignature(msg *whisper.ReceivedMessage, id []byte) error {
	src := crypto.FromECDSAPub(msg.Src)
	if len(src)-len(id) == 1 {
		src = src[1:]
	}

	// if you want to check the signature, you can do it here. e.g.:
	// if !bytes.Equal(peerID, src) {
	if src == nil {
		return errors.New("Wrong signature of p2p request")
	}

	return nil
}

// bloomFromReceivedMessage for a given whisper.ReceivedMessage it extracts the
// used bloom filter.
func (s *WMailServer) bloomFromReceivedMessage(msg *whisper.ReceivedMessage) ([]byte, error) {
	payloadSize := len(msg.Payload)

	if payloadSize < 8 {
		return nil, errors.New("Undersized p2p request")
	} else if payloadSize == 8 {
		return whisper.MakeFullNodeBloom(), nil
	} else if payloadSize < 8+whisper.BloomFilterSize {
		return nil, errors.New("Undersized bloom filter in p2p request")
	}

	return msg.Payload[8 : 8+whisper.BloomFilterSize], nil
}

// peerWithID is a generalization of whisper.Peer.
// whisper.Peer has all fields and methods, except for ID(), unexported.
// It makes it impossible to create an instance of it
// outside of whisper package and test properly.
type peerWithID interface {
	ID() []byte
}

func peerIDString(peer peerWithID) string {
	return fmt.Sprintf("%x", peer.ID())
}

func peerIDBytesString(id []byte) string {
	return fmt.Sprintf("%x", id)
}
func perform(mailserver *WMailServer) {
	upper := uint32(1555329600)
	lower := uint32(1554724800)

	iter := mailserver.createIterator(lower, upper, nil)
	defer iter.Release()
	count := 0
	start := time.Now().UnixNano()

	bloom := make([]byte, 64)

	for iter.Prev() {
		var envelope whisper.Envelope
		decodeErr := rlp.DecodeBytes(iter.Value(), &envelope)
		if decodeErr != nil {
			log.Error("[mailserver:processRequestInBundles] failed to decode RLP",
				"err", decodeErr)
			continue
		}
		if !whisper.BloomFilterMatch(bloom, envelope.Bloom()) {
			continue
		}

		count += 1
	}
	end := time.Now().UnixNano()
	fmt.Println(count)
	fmt.Println("TIME", (end-start)/1e6)
}

func perform4(mailserver *WMailServer, query []byte, _ *sql.DB) {
	upper := uint32(1555329600)
	lower := uint32(1554724800)
	//upper := uint32(1655329600)
	//lower := uint32(0)

	allCount := 0

	iter := mailserver.createIterator(lower, upper, nil)
	defer iter.Release()
	count := 0
	start := time.Now().UnixNano()

	for iter.Prev() {
		var envelope whisper.Envelope
		decodeErr := rlp.DecodeBytes(iter.Value(), &envelope)
		if decodeErr != nil {
			log.Error("[mailserver:processRequestInBundles] failed to decode RLP",
				"err", decodeErr)
			continue
		}
		allCount += 1
		if !whisper.BloomFilterMatch(query, envelope.Bloom()) {
			continue
		}

		count += 1
	}
	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)
}

func perform8(mailserver *WMailServer, query []byte, _ *sql.DB) {
	//upper := uint32(1555329600)
	//lower := uint32(1554724800)
	upper := uint32(1655329600)
	lower := uint32(0)

	allCount := 0

	iter := mailserver.createIterator(lower, upper, nil)
	defer iter.Release()
	count := 0
	start := time.Now().UnixNano()

	for iter.Prev() {
		allCount += 1
		count += 1
	}
	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)
}

func perform5(mailserver *WMailServer, query []byte, db *sql.DB) {
	upper := uint32(1555329600)
	lower := uint32(1554724800)
	allCount := 0

	count := 0
	start := time.Now().UnixNano()
	stmt, err := db.Prepare("SELECT data FROM envelopes where timestamp between $1 AND $2")
	if err != nil {
		fmt.Println("ERROR PREPARING", err)
	}

	rows, err := stmt.Query(lower, upper)
	if err != nil {
		fmt.Println("ERROR RUNNING", err)
	}

	for rows.Next() {
		allCount += 1
		var envelope whisper.Envelope
		var rawEnvelope []byte
		err = rows.Scan(&rawEnvelope)
		decodeErr := rlp.DecodeBytes(rawEnvelope, &envelope)
		if decodeErr != nil {
			log.Error("[mailserver:processRequestInBundles] failed to decode RLP",
				"err", decodeErr)
			continue
		}
		if !whisper.BloomFilterMatch(query, envelope.Bloom()) {
			continue
		}

		count += 1
	}
	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)
}

func perform10(mailserver *WMailServer, query []byte, db *sql.DB) {
	upper := uint32(1655329600)
	lower := uint32(0)
	allCount := 0

	count := 0
	start := time.Now().UnixNano()
	stmtString := fmt.Sprintf("SELECT data FROM envelopes2 where timestamp between $1 AND $2 AND bloom & b'%s'::bit(512) = bloom", toBitString(query))

	stmt, err := db.Prepare(stmtString)
	if err != nil {
		fmt.Println("ERROR PREPARING", err)
	}

	rows, err := stmt.Query(lower, upper)
	if err != nil {
		fmt.Println("ERROR RUNNING", err)
	}

	for rows.Next() {
		allCount += 1
		var rawEnvelope []byte
		err = rows.Scan(&rawEnvelope)
		count += 1
	}
	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)
}

type QueryTopic struct {
	topic whisper.TopicType
	bytes []byte
	bloom []byte
}

func perform7(mailserver *WMailServer, query []byte, db *sql.DB) {
	//upper := uint32(1555329600)
	//lower := uint32(1554724800)
	upper := uint32(1655329600)
	lower := uint32(0)

	allCount := 0

	var topics []*QueryTopic

	stmt1, err := db.Prepare("SELECT distinct(topic) FROM envelopes")
	if err != nil {
		fmt.Println("ERROR PREPARING", err)
	}

	rows, err := stmt1.Query()
	if err != nil {
		fmt.Println("ERROR RUNNING", err)
	}

	for rows.Next() {
		topic := &QueryTopic{}

		err = rows.Scan(&topic.bytes)
		if err != nil {
			fmt.Println("ERROR SCANNING", err)
		}
		topic.topic = whisper.BytesToTopic(topic.bytes)
		topic.bloom = topicsToBloom(topic.topic)
		topics = append(topics, topic)

	}
	for i := range makeRange(0, 1) {
		bs := make([]byte, 4)
		binary.LittleEndian.PutUint32(bs, uint32(i))
		topic := &QueryTopic{}
		topic.bytes = bs
		topic.topic = whisper.BytesToTopic(bs)
		topic.bloom = topicsToBloom(topic.topic)
		topics = append(topics, topic)

	}
	start := time.Now().UnixNano()

	var queryTopics []*QueryTopic
	for _, t := range topics {
		if whisper.BloomFilterMatch(query, t.bloom) {
			queryTopics = append(queryTopics, t)
		}
	}

	fmt.Println("Got iterator", len(queryTopics))
	count := 0

	//args := make([]interface{}, len(queryTopics)+2)
	args := make([]interface{}, 2)
	args[0] = lower
	args[1] = upper

	var rawEnvelopes []whisper.Envelope

	batch := 5000
	for i := 0; i < len(queryTopics); i += batch {
		j := i + batch
		if j > len(queryTopics) {
			j = len(queryTopics)
		}

		statementString := "SELECT data FROM envelopes where timestamp between $1 AND $2 AND TOPIC IN ("
		statementString += fmt.Sprintf("E'\\\\x%x'", queryTopics[0].bytes)

		for _, t := range queryTopics[i+1 : j] {
			statementString += fmt.Sprintf(",E'\\\\x%x'", t.bytes)
			//		args[i+2] = t.bytes
		}

		statementString += ") ORDER BY TIMESTAMP"

		stmt2, err := db.Prepare(statementString)
		if err != nil {
			fmt.Println("ERROR PREPARING", err)
		}

		if err != nil {
			fmt.Println("ERROR PREPARING", err)
		}

		rows, err = stmt2.Query(args...)
		if err != nil {
			fmt.Println("ERROR RUNNING", err)
		}

		for rows.Next() {
			allCount += 1
			var rawEnvelope []byte
			err = rows.Scan(&rawEnvelope)
			count += 1
		}
	}

	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)

	fmt.Println(time.Now().Unix())
	for _, envelope := range rawEnvelopes {
		if whisper.BloomFilterMatch(query, envelope.Bloom()) {
			continue

		}

	}
	fmt.Println(time.Now().Unix())

}
func perform9(mailserver *WMailServer, query []byte, db *sql.DB) {
	upper := uint32(1655329600)
	lower := uint32(0)
	allCount := 0

	var topics []*QueryTopic

	stmt1, err := db.Prepare("SELECT distinct(topic) FROM envelopes")
	if err != nil {
		fmt.Println("ERROR PREPARING", err)
	}

	rows, err := stmt1.Query()
	if err != nil {
		fmt.Println("ERROR RUNNING", err)
	}

	for rows.Next() {
		topic := &QueryTopic{}

		err = rows.Scan(&topic.bytes)
		if err != nil {
			fmt.Println("ERROR SCANNING", err)
		}
		topic.topic = whisper.BytesToTopic(topic.bytes)
		topic.bloom = topicsToBloom(topic.topic)
		topics = append(topics, topic)

	}
	for i := range makeRange(0, 1) {
		bs := make([]byte, 4)
		binary.LittleEndian.PutUint32(bs, uint32(i))
		topic := &QueryTopic{}
		topic.bytes = bs
		topic.topic = whisper.BytesToTopic(bs)
		topic.bloom = topicsToBloom(topic.topic)
		topics = append(topics, topic)

	}
	start := time.Now().UnixNano()

	fmt.Println(time.Now().UnixNano())
	var queryTopics []*QueryTopic
	for _, t := range topics {
		if whisper.BloomFilterMatch(query, t.bloom) {
			queryTopics = append(queryTopics, t)
		}
	}
	fmt.Println(time.Now().UnixNano())

	fmt.Println("Got iterator", len(queryTopics))
	count := 0

	//args := make([]interface{}, len(queryTopics)+2)
	args := make([]interface{}, 2)
	args[0] = lower
	args[1] = upper

	batch := 5000
	for i := 0; i < len(queryTopics); i += batch {
		j := i + batch
		if j > len(queryTopics) {
			j = len(queryTopics)
		}

		statementString := "SELECT data FROM envelopes where timestamp between $1 AND $2 AND TOPIC IN ("
		statementString += fmt.Sprintf("E'\\\\x%x'", queryTopics[0].bytes)

		for _, t := range queryTopics[i+1 : j] {
			statementString += fmt.Sprintf(",E'\\\\x%x'", t.bytes)
			//		args[i+2] = t.bytes
		}

		statementString += ") ORDER BY TIMESTAMP"

		stmt2, err := db.Prepare(statementString)
		if err != nil {
			fmt.Println("ERROR PREPARING", err)
		}

		if err != nil {
			fmt.Println("ERROR PREPARING", err)
		}

		rows, err = stmt2.Query(args...)
		if err != nil {
			fmt.Println("ERROR RUNNING", err)
		}

		for rows.Next() {
			allCount += 1
			var rawEnvelope []byte
			err = rows.Scan(&rawEnvelope)
			count += 1
		}
	}

	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)
}

func perform6(mailserver *WMailServer, query []byte, db *sql.DB) {
	upper := uint32(1655329600)
	lower := uint32(0)
	allCount := 0

	var topics []*QueryTopic

	stmt1, err := db.Prepare("SELECT distinct(topic) FROM envelopes")
	if err != nil {
		fmt.Println("ERROR PREPARING", err)
	}

	rows, err := stmt1.Query()
	if err != nil {
		fmt.Println("ERROR RUNNING", err)
	}

	for rows.Next() {
		topic := &QueryTopic{}

		err = rows.Scan(&topic.bytes)
		if err != nil {
			fmt.Println("ERROR SCANNING", err)
		}
		topic.topic = whisper.BytesToTopic(topic.bytes)
		topic.bloom = topicsToBloom(topic.topic)
		topics = append(topics, topic)

	}
	for i := range makeRange(0, 1) {
		bs := make([]byte, 4)
		binary.LittleEndian.PutUint32(bs, uint32(i))
		topic := &QueryTopic{}
		topic.bytes = bs
		topic.topic = whisper.BytesToTopic(bs)
		topic.bloom = topicsToBloom(topic.topic)
		topics = append(topics, topic)

	}
	start := time.Now().UnixNano()

	fmt.Println(time.Now().UnixNano())
	var queryTopics []*QueryTopic
	for _, t := range topics {
		if whisper.BloomFilterMatch(query, t.bloom) {
			queryTopics = append(queryTopics, t)
		}
	}
	fmt.Println(time.Now().UnixNano())

	fmt.Println("Got iterator", len(queryTopics))
	count := 0

	//args := make([]interface{}, len(queryTopics)+2)
	args := make([]interface{}, 2)
	args[0] = lower
	args[1] = upper

	batch := 5000
	for i := 0; i < len(queryTopics); i += batch {
		j := i + batch
		if j > len(queryTopics) {
			j = len(queryTopics)
		}

		statementString := "SELECT data FROM envelopes where timestamp between $1 AND $2 AND TOPIC IN ("
		statementString += fmt.Sprintf("E'\\\\x%x'", queryTopics[0].bytes)

		for _, t := range queryTopics[i+1 : j] {
			statementString += fmt.Sprintf(",E'\\\\x%x'", t.bytes)
			//		args[i+2] = t.bytes
		}

		statementString += ") ORDER BY TIMESTAMP"

		stmt2, err := db.Prepare(statementString)
		if err != nil {
			fmt.Println("ERROR PREPARING", err)
		}

		if err != nil {
			fmt.Println("ERROR PREPARING", err)
		}

		rows, err = stmt2.Query(args...)
		if err != nil {
			fmt.Println("ERROR RUNNING", err)
		}

		for rows.Next() {
			allCount += 1
			var envelope whisper.Envelope
			var rawEnvelope []byte
			err = rows.Scan(&rawEnvelope)
			decodeErr := rlp.DecodeBytes(rawEnvelope, &envelope)
			if decodeErr != nil {
				log.Error("[mailserver:processRequestInBundles] failed to decode RLP",
					"err", decodeErr)
				continue
			}
			count += 1
		}
	}

	end := time.Now().UnixNano()
	fmt.Println(count, allCount)
	fmt.Println("TIME", (end-start)/1e6)
}

type Partition struct {
	bloom  []byte
	topics []whisper.TopicType
}

func topicToByte(t whisper.TopicType) []byte {
	return []byte{t[0], t[1], t[2], t[3]}
}

func toBitString(bloom []byte) string {
	val := ""
	for _, n := range bloom {
		val += fmt.Sprintf("%08b", n)
	}
	return val
}

func perform3(mailserver *WMailServer, query []byte, db *sql.DB) {
	upper := uint32(1655329600)
	lower := uint32(0)
	//var seconds uint32 = 86400
	var seconds uint32 = 3600

	_, err := db.Exec("CREATE TABLE IF NOT EXISTS envelopes (timestamp INT NOT NULL, data BYTEA NOT NULL, topic BYTEA NOT NULL, id BYTEA NOT NULL UNIQUE)")
	if err != nil {
		fmt.Println("ERROR CREATING", err)
		return
	}

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS envelopes2 (timestamp INT NOT NULL, data BYTEA NOT NULL, topic BYTEA NOT NULL, id BYTEA NOT NULL UNIQUE, bloom BIT(512) NOT NULL)")
	if err != nil {
		fmt.Println("ERROR CREATING", err)
		return
	}

	iter := mailserver.createIterator(lower, upper, nil)
	fmt.Println("Got iterator")
	defer iter.Release()
	count := 0
	start := time.Now().UnixNano()

	bloom := make([]byte, 64)
	topicMap := make(map[int]*Partition)
	allTopics := make(map[string]whisper.TopicType)

	for iter.Prev() {
		var envelope whisper.Envelope
		rawEnvelope := iter.Value()
		decodeErr := rlp.DecodeBytes(rawEnvelope, &envelope)

		if decodeErr != nil {
			log.Error("[mailserver:processRequestInBundles] failed to decode RLP",
				"err", decodeErr)
			continue
		}
		stmtString := `INSERT INTO envelopes2 VALUES ($1, $2, $3, $4, B'`
		stmtString += toBitString(envelope.Bloom())
		stmtString += `'::bit(512)) ON CONFLICT (id) DO NOTHING;`
		stmt, err := db.Prepare(stmtString)
		if err != nil {
			fmt.Println("ERROR PREPARING", err)
			return
		}
		_, err = stmt.Exec(
			envelope.Expiry-envelope.TTL,
			rawEnvelope,
			topicToByte(envelope.Topic),
			envelope.Hash(),
		)

		if err != nil {
			fmt.Println("ERROR INSERTING", err)
			return
		}

		stmt.Close()

		partition := int(envelope.Expiry / seconds)
		if _, ok := topicMap[partition]; !ok {
			topicMap[partition] = &Partition{}
		}

		allTopics[envelope.Topic.String()] = envelope.Topic
		topicMap[partition].topics = append(topicMap[partition].topics, envelope.Topic)
		if !whisper.BloomFilterMatch(bloom, envelope.Bloom()) {
			continue
		}

		count += 1
	}
	fmt.Println("TOPICS")
	fmt.Println(len(topicMap))
	matches := 0
	for _, val := range topicMap {

		val.bloom = topicsToBloom(val.topics...)
		if whisper.BloomFilterMatch(query, val.bloom) {
			matches += 1
		}

	}
	fmt.Println("MATCHES")
	fmt.Println(matches)
	var allTopics2 []whisper.TopicType
	//for i := range makeRange(0, 10967296) {
	//	bs := make([]byte, 4)
	//	binary.LittleEndian.PutUint32(bs, uint32(i))
	//	allTopics2 = append(allTopics2, whisper.BytesToTopic(bs))
	//
	//	}

	fmt.Println("STARTING")
	start2 := time.Now().UnixNano()

	var queryTopics []whisper.TopicType
	for _, val := range allTopics2 {

		bloom := topicsToBloom(val)
		if whisper.BloomFilterMatch(query, bloom) {
			queryTopics = append(queryTopics, val)
		}

	}

	end2 := time.Now().UnixNano()

	fmt.Println(end2 - start2)

	fmt.Println("QUERY TOPICS")
	fmt.Println(len(queryTopics))

	end := time.Now().UnixNano()
	fmt.Println(count)
	fmt.Println("TIME", (end-start)/1e6)
}

func combinations(iterable []int, r int) [][]int {
	pool := iterable
	n := len(pool)
	var response [][]int

	if r > n {
		return nil
	}

	indices := make([]int, r)
	for i := range indices {
		indices[i] = i
	}

	result := make([]int, r)
	for i, el := range indices {
		result[i] = pool[el]
	}

	for {
		i := r - 1
		for ; i >= 0 && indices[i] == i+n-r; i -= 1 {
		}

		if i < 0 {
			return response
		}

		indices[i] += 1
		for j := i + 1; j < r; j += 1 {
			indices[j] = indices[j-1] + 1
		}

		for ; i < len(indices); i += 1 {
			result[i] = pool[indices[i]]
		}

		tmp := make([]int, len(result))
		copy(tmp, result)
		response = append(response, tmp)

	}
	return response

}

func makeRange(min, max int) []int {
	a := make([]int, max-min+1)
	for i := range a {
		a[i] = min + i
	}
	return a
}

func topicsToBloom(topics ...whisper.TopicType) []byte {
	i := new(big.Int)
	for _, topic := range topics {
		bloom := whisper.TopicToBloom(topic)
		i.Or(i, new(big.Int).SetBytes(bloom[:]))
	}

	combined := make([]byte, whisper.BloomFilterSize)
	data := i.Bytes()
	copy(combined[whisper.BloomFilterSize-len(data):], data[:])

	return combined
}

func main() {
	mailserver := &WMailServer{}
	err := mailserver.Init()
	if err != nil {
		fmt.Printf("ERROR opening db: %+v\n", err)
		return
	}

	topicStrings := []string{"cd423760", "e1e1f75b", "9295f3ff"}

	topicStrings = topicStrings[:len(topicStrings)]
	var topics []whisper.TopicType
	for _, topicString := range topicStrings {
		topicByte, err := hex.DecodeString(topicString)
		if err != nil {
			fmt.Println(err)
			return
		}
		topics = append(topics, whisper.BytesToTopic(topicByte))
	}
	bloom := topicsToBloom(topics...)
	response := combinations(makeRange(0, 63), 3)
	//fmt.Println(response)
	count := 0
	for _, combination := range response {
		if bloom[combination[0]] != 0 && bloom[combination[1]] != 0 && bloom[combination[2]] != 0 {
			count = count + 1
		}
	}
	connStr := "postgres://postgres:example@localhost/postgres?sslmode=disable"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fmt.Println("ERROR CONNECTING", err)
		return
	}
	fmt.Println(whisper.BytesToTopic(crypto.Keccak256([]byte("status"))))
	fmt.Println(len(topicStrings))
	//perform6(mailserver, bloom, db)
	//perform7(mailserver, bloom, db)
	//perform10(mailserver, bloom, db)
	//perform4(mailserver, bloom, db)
	//perform5(mailserver, bloom, db)
	//perform6(mailserver, bloom, db)
	//perform7(mailserver, bloom, db)
	//perform3(mailserver, bloom, db)
	//go perform5(mailserver, bloom, db)
	//go perform5(mailserver, bloom, db)
	//go perform5(mailserver, bloom, db)
	//go perform5(mailserver, bloom, db)
	//go perform5(mailserver, bloom, db)

	//	go perform4(mailserver, bloom)
	//	go perform4(mailserver, bloom)
	//	go perform4(mailserver, bloom)
	//	go perform4(mailserver, bloom)

	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)

	//go perform(mailserver, client)

	f := perform10
	go f(mailserver, bloom, db)
	go f(mailserver, bloom, db)
	go f(mailserver, bloom, db)
	go f(mailserver, bloom, db)
	//go perform6(mailserver, bloom, db)
	//go perform6(mailserver, bloom, db)
	//go perform6(mailserver, bloom, db)
	//go perform6(mailserver, bloom, db)
	//go perform6(mailserver, bloom, db)
	time.Sleep(time.Second * 60)
}
