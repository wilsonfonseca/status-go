package main

import (
	"context"
	"encoding/hex"
	"flag"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/crypto/sha3"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/whisper/shhclient"
	whisper "github.com/ethereum/go-ethereum/whisper/whisperv6"
	"github.com/status-im/status-go/logutils"
	"github.com/status-im/status-go/node"
	"github.com/status-im/status-go/params"
	"github.com/status-im/status-go/services/shhext"
	"github.com/status-im/status-go/t/helpers"
	"github.com/status-im/whisper/whisperv6"
)

var (
	mailserver = flag.String("mailserver", "", "MailServer address (by default a random one from the fleet is selected)")
	duration   = flag.Duration("duration", time.Hour*(24*7), "length of time span from now")
	channel    = flag.String("channel", "status", "name of the channel")
)

func init() {
	if err := logutils.OverrideRootLog(true, "INFO", "", true); err != nil {
		log.Crit("failed to override root log", "error", err)
	}
}

func init() {
	flag.Parse()
}

func newNodeConfig() (*params.NodeConfig, error) {
	c, err := params.NewNodeConfigWithDefaults("/tmp/xxx", params.MainNetworkID, params.WithFleet(params.FleetBeta))
	if err != nil {
		return nil, err
	}

	c.ListenAddr = "" // don't start a listener
	c.MaxPeers = 10
	c.IPCEnabled = true
	c.HTTPEnabled = false

	// no other connection except that we want
	c.NoDiscovery = true
	c.Rendezvous = false
	c.ClusterConfig.StaticNodes = nil

	return c, nil
}

func main() {
	config, err := newNodeConfig()
	if err != nil {
		log.Crit("failed to create a config", "error", err)
	}
	n := node.New()
	if err := n.Start(config); err != nil {
		log.Crit("failed to start a node", "error", err)
	}

	rpcClient, err := n.GethNode().Attach()
	if err != nil {
		log.Crit("failed to get an rpc", "error", err)
	}
	shh := shhclient.NewClient(rpcClient)

	shhextService, err := n.ShhExtService()
	if err != nil {
		log.Crit("failed go get an shhext service", "error", err)
	}
	shhextAPI := shhext.NewPublicAPI(shhextService)

	shhService, err := n.WhisperService()
	if err != nil {
		log.Crit("failed to get whisper service", "error", err)
	}
	whispersEvents := make(chan whisperv6.EnvelopeEvent, 10)
	whisperSub := shhService.SubscribeEnvelopeEvents(whispersEvents)
	defer whisperSub.Unsubscribe()

	symKeyID, err := addPublicChatSymKey(shh, *channel)
	if err != nil {
		log.Crit("failed to add sym key for channel", "channel", *channel, "error", err)
	}

	messages := make(chan *whisperv6.Message)
	filterID, err := subscribeMessages(shh, *channel, symKeyID)
	if err != nil {
		log.Crit("failed to subscribe to messages", "channel", *channel, "error", err)
	}

	log.Info("Adding mail server as a peer", "address", *mailserver)

	mailserverEnode := *mailserver
	if mailserverEnode == "" {
		mailserverEnode = config.ClusterConfig.TrustedMailServers[rand.Intn(len(config.ClusterConfig.TrustedMailServers))]
	}

	if err := n.AddPeer(mailserverEnode); err != nil {
		log.Crit("failed to add Mail Server as a peer", "error", err)
	}

	errCh := helpers.WaitForPeerAsync(n.Server(), mailserverEnode, p2p.PeerEventTypeAdd, 5*time.Second)
	if err := <-errCh; err != nil {
		log.Crit("failed to wait for peer", "peer", mailserverEnode, "error", err)
	}

	log.Info("sending requests to Mail Server")

	topic, err := PublicChatTopic([]byte(*channel))
	if err != nil {
		log.Crit("failed to get topic", "channel", *channel, "error", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mailServerSymKeyID, err := shh.GenerateSymmetricKeyFromPassword(ctx, MailServerPassword)
	if err != nil {
		log.Info("failed to generate sym key for mail server", "error", err)
	}

	hash, err := shhextAPI.RequestMessages(nil, shhext.MessagesRequest{
		MailServerPeer: mailserverEnode,
		SymKeyID:       mailServerSymKeyID,
		From:           uint32(time.Now().Add(-*duration).Unix()),
		Topic:          whisperv6.TopicType(topic),
		Timeout:        120,
	})
	sent := time.Now()
	if err != nil {
		log.Crit("failed to request for messages", "error", err)
	}
	log.Info("requested for messages with a request", "hash", hash)

	for {
		select {
		case msg := <-messages:
			source := hex.EncodeToString(msg.Sig)
			log.Info("received a message: topic=%v data=%s author=%s", msg.Topic, msg.Payload, source)
		case ev := <-whispersEvents:
			switch ev.Event {
			case whisperv6.EventMailServerRequestSent:
				log.Info("request sent at", "time", sent)
			case whisperv6.EventMailServerRequestCompleted:
				finished := time.Since(sent)
				log.Info("request finished after", "duration", finished)
				messages, err := shh.FilterMessages(context.TODO(), filterID)
				if err != nil {
					log.Crit("failed to get messages from a filter", "error", err)
				}
				log.Info("total messages", "TOTAL", len(messages))
				return
			case whisperv6.EventMailServerRequestExpired:
				log.Error("request expired, continue waiting")
			}
		}
	}

}

func addPublicChatSymKey(c *shhclient.Client, chat string) (string, error) {
	// This operation can be really slow, hence 10 seconds timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.GenerateSymmetricKeyFromPassword(ctx, chat)
}

func subscribeMessages(c *shhclient.Client, chat, symKeyID string) (string, error) {
	topic, err := PublicChatTopic([]byte(chat))
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.NewMessageFilter(ctx, whisper.Criteria{
		SymKeyID: symKeyID,
		MinPow:   0,
		Topics:   []whisper.TopicType{topic},
		AllowP2P: true,
	})
}

const (
	// MailServerPassword is required to make requests to MailServers.
	MailServerPassword = "status-offline-inbox"
)

// PublicChatTopic returns a Whisper topic for a public channel name.
func PublicChatTopic(name []byte) (whisper.TopicType, error) {
	hash := sha3.NewKeccak256()
	if _, err := hash.Write(name); err != nil {
		return whisper.TopicType{}, err
	}

	return whisper.BytesToTopic(hash.Sum(nil)), nil
}
