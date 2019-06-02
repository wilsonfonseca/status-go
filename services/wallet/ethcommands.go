package wallet

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

func makeERC20HistoricalCommand(db *Database, client HeaderReader, erc20 BatchDownloader, feed *event.Feed) func(context.Context) error {
	var (
		iterator *IterativeDownloader
		err      error
	)
	return func(ctx context.Context) error {
		if iterator == nil {
			iterator, err = SetupIterativeDownloader(db, client, erc20Sync, erc20, erc20BatchSize)
			if err != nil {
				log.Error("failed to setup historical downloader for erc20")
				return err
			}
		}
		return ERC20HistoricalCommand(ctx, iterator, db, feed)
	}
}

func ERC20HistoricalCommand(parent context.Context, iterator *IterativeDownloader, db *Database, feed *event.Feed) (err error) {
	for !iterator.Finished() {
		transfers, err := iterator.Next()
		if err != nil {
			log.Error("failed to get next batch", "error", err)
			return err
		}
		headers := headersFromTransfers(transfers)
		headers = append(headers, iterator.Header())
		err = db.ProcessTranfers(transfers, headers, nil, erc20Sync)
		if err != nil {
			iterator.Revert()
			log.Error("failed to save downloaded erc20 transfers", "error", err)
			return err
		}
		feed.Send(Event{
			Type:        EventNewHistory,
			BlockNumber: iterator.Header().Number,
		})
	}
	log.Info("wallet historical downloader for erc20 transfers finished")
	return nil
}

func makeETHHistoricalCommand(db *Database, client HeaderReader, eth BatchDownloader, feed *event.Feed) func(context.Context) error {
	var (
		iterator *IterativeDownloader
		err      error
	)
	return func(ctx context.Context) error {
		if iterator == nil {
			iterator, err = SetupIterativeDownloader(db, client, ethSync, eth, ethBatchSize)
			if err != nil {
				log.Error("failed to setup historical downloader for eth")
				return err
			}
		}
		return ETHHistoricalCommand(ctx, iterator, db, feed)
	}
}

func ETHHistoricalCommand(parent context.Context, iterator *IterativeDownloader, db *Database, feed *event.Feed) (err error) {
	for !iterator.Finished() {
		transfers, err := iterator.Next()
		if err != nil {
			log.Error("failed to get next batch", "error", err)
			return err
		}
		headers := headersFromTransfers(transfers)
		headers = append(headers, iterator.Header())
		err = db.ProcessTranfers(transfers, headers, nil, ethSync)
		if err != nil {
			iterator.Revert()
			log.Error("failed to save downloaded eth transfers", "error", err)
			return err
		}
		feed.Send(Event{
			Type:        EventNewHistory,
			BlockNumber: iterator.Header().Number,
		})
	}
	return nil
}

func makeNewTransfersCommand(
	db *Database, client HeaderReader,
	erc20, eth BatchDownloader, feed *event.Feed) func(context.Context) error {
	var (
		previous *DBHeader
		err      error
	)
	return func(ctx context.Context) error {
		if previous == nil {
			previous, err = lastKnownHeader(db, client)
			if err != nil {
				log.Error("failed to get last known header", "error", err)
				return err
			}
		}
	}
}

func NewTransfersCommand(parent context.Context, previous *DBHeader, db *Database, client HeaderReader, feed *event.Feed) (err error) {
	num = num.Add(previous.Number, one)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	latest, err := client.HeaderByNumber(ctx, num)
	cancel()
	if err != nil {
		log.Warn("failed to get latest block", "number", num, "error", err)
		return
	}
	log.Debug("reactor received new block", "header", latest.Hash())
	ctx, cancel = context.WithTimeout(parent, 10*time.Second)
	added, removed, err := r.onNewBlock(ctx, previous, latest)
	cancel()
	if err != nil {
		log.Error("failed to process new header", "header", latest, "error", err)
		continue
	}
	// for each added block get tranfers from downloaders
	all := []Transfer{}
	for i := range added {
		log.Debug("reactor get transfers", "block", added[i].Hash, "number", added[i].Number)
		transfers, err := r.getTransfers(added[i])
		if err != nil {
			log.Error("failed to get transfers", "header", added[i].Hash, "error", err)
			continue
		}
		log.Debug("reactor adding transfers", "block", added[i].Hash, "number", added[i].Number, "len", len(transfers))
		all = append(all, transfers...)
	}
	err = db.ProcessTranfers(all, added, removed, erc20Sync|ethSync)
	if err != nil {
		log.Error("failed to persist transfers", "error", err)
		continue
	}
	previous = toDBHeader(latest)
	if len(added) == 1 && len(removed) == 0 {
		r.feed.Send(Event{
			Type:        EventNewBlock,
			BlockNumber: added[0].Number,
		})
	}
	if len(removed) != 0 {
		lth := len(removed)
		feed.Send(Event{
			Type:        EventReorg,
			BlockNumber: removed[lth-1].Number,
		})
	}
	// TODO(dshulyak) implement type for infinite commands.
	return errors.New("continue")
}
