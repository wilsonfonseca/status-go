package main

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/status-im/status-go/api"
	"github.com/status-im/status-go/params"
)

type State int

const (
	StateStarted    State = 1 >> iota
	StateRestarting State = 1 >> iota
	StateStopped    State = 1 >> iota
)

func NewStatusNode() *StatusNode {
	node := &StatusNode{
		state:   StateRestarting,
		backend: api.NewStatusBackend(),
	}

	return node
}

type StatusNode struct {
	backend          *api.StatusBackend
	state            State
	confOverrideJSON []string
}

func (n *StatusNode) Start() error {
	fmt.Println("starting node....")
	err := n.backend.StartNode(n.GetConfig())
	if err == nil {
		n.state = StateStarted
	} else {
		n.state = StateStopped
		panic(err)
	}
	fmt.Println("node ready")
	return err
}

func (n *StatusNode) Stop() error {
	err := n.backend.StopNode()
	if err == nil {
		n.state = StateStopped
	} else {
		n.state = StateStarted
		panic(err)
	}
	return err
}

func (n *StatusNode) State() State {
	return n.state
}

func (n *StatusNode) MailserverRunning() bool {
	return n.backend.StatusNode().Config().WhisperConfig.EnableMailServer
}

func (n *StatusNode) StaticPeers() []*enode.Node {
	return n.backend.StatusNode().GethNode().Server().StaticNodes
}

func (n *StatusNode) EnodeID() string {
	return n.backend.StatusNode().GethNode().Server().NodeInfo().Enode
}

func (n *StatusNode) MailserverEnode() string {
	// TODO: a hack
	enodeID := n.EnodeID()

	elements := strings.Split(enodeID, "@")

	password := n.backend.StatusNode().Config().WhisperConfig.MailServerPassword

	return fmt.Sprintf("%s:%s@%s", elements[0], password, elements[1])
}

func (n *StatusNode) AddOverride(override string) {
	n.confOverrideJSON = append(n.confOverrideJSON, override)
}

func (n *StatusNode) GetConfig() *params.NodeConfig {
	opts := []params.Option{params.WithFleet(params.FleetBeta)}
	opts = append(opts, params.WithMailserver())
	for _, override := range n.confOverrideJSON {
		opts = append(opts, params.WithJSON(override))
	}

	conf, err := params.NewNodeConfigWithDefaults("status_node_data_dir", 1, opts...)
	if err != nil {
		// TODO: fix
		panic(err)
	}

	conf.RegisterTopics = append(conf.RegisterTopics, params.MailServerDiscv5Topic)
	return conf
}
