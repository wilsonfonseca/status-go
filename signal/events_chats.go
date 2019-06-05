package signal

const (
	// EventChatsDidChange is triggered when there is new data in any of the subscriptions
	EventChatsDidChange = "status.chats.did-change"
	EventNewMessages    = "status.chats.new-messages"
)

func SendChatsDidChangeEvent(name string) {
	send(EventChatsDidChange, name)
}

func SendNewMessagesSignal(messagesInfo interface{}) {
	send(EventNewMessages, messagesInfo)
}
