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
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

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
	database, err := db.Open("/docker/statusd-mail/data/wnode", nil)
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
	fmt.Println("Got iterator")
	defer iter.Release()
	count := 0
	start := time.Now().Unix()

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
	end := time.Now().Unix()
	fmt.Println(count)
	fmt.Println(end - start)
}

func main() {
	mailserver := &WMailServer{}
	err := mailserver.Init()
	if err != nil {
		fmt.Printf("ERROR opening db: %+v\n", err)
		return
	}

	perform(mailserver)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)
	//go perform(mailserver, client)

	//time.Sleep(time.Second * 30)
}
