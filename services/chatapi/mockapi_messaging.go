package chatapi

import (
	"fmt"
	"sync"
	"time"

	"github.com/status-im/status-go/signal"
)

// "id:<message-id>"
// "limit:100"
type Cursor string

/*

alt:

GetMessagesBefore(id, limit) // 0 -- all
GetMessagesAfter(id, limit)
GetMessagesBetween(id1, id2)

*/

type ChatElement interface{}

/*

:old-message-id"0x53f8ced4c2f5c334d8484a51f13676b07b265518e0ccf347d81078b02188362e"
:message-id"0x5af50cad20e685a4867fbb65fe2ef765d20781889ab692d7e2245f5863436d8b"

:last?true // if that's the newes message

:content {3 keys}
	:chat-id"status"
	:text"50,000 people used to live here.... MW2 🎮"
	:response-to {2 keys}
		:from"0x0461214276fd5a0476430d8bd3e80899a69c6dd3ee4f780a2c66f2eeebbd7a14cd0183554b6fa5bbc8a3526cfaf99f1def4f8ccb7ac546ef5c6bf9b659c05ccfc0"
		:text"It's like a ghost town in here"

// UNUSED?
:js-obj {8 keys}
	"sig""0x04c940125c0b746c44dad3ef29a15a567fe63f291fa70bb06f0167c1711de2f13bd2dffb3932755775e06886ea3da91ccc5b9b44c8568c62d8e8b08a8bcdc168b3"
	"ttl"10
	"timestamp"1559283119
	"topic""0xcd423760"
	"payload""0x5b227e236334222c5b2235302c3030302070656f706c65207573656420746f206c69766520686572652e2e2e2e204d573220f09f8eae222c22746578742f706c61696e222c227e3a7075626c69632d67726f75702d757365722d6d657373616765222c3135363134353538313232313435382c313535393238333131393038322c5b225e20222c227e3a636861742d6964222c22737461747573222c227e3a74657874222c2235302c3030302070656f706c65207573656420746f206c69766520686572652e2e2e2e204d573220f09f8eae222c227e3a726573706f6e73652d746f222c22307863666338623338386535373733386430333032356437653566343361343135363965663465333064393264353866373032396366393335326633663338303434222c227e3a726573706f6e73652d746f2d7632222c22307863353765323965326333303433666332356463356135356564643132346433653032663563626339366435643537613264653561663335376364633933353439225d5d5d"
	"padding""0xfc7a1b37878ae20f516f7a337bfd48f0d5dbfc12873a067bec8e696d82fc302b4672bdafb82814df14354888d23d30354ff1767add532a81d2"
	"pow"0.002857142857142857
	"hash""0x5aa2ddf61eccfaaea495fd1a7da7d63cf3396ceae3cff8086b03e9cbf735295c"


:user-statuses {1 keys}
	"0x04c7fcb5a2e5a01dc839b582186717c31a6b01d3c0f7790c938f268a0153dc1fcb69e04c2a8bd263f80c03fd27d74924819432e2552baa03afef2f3f67378c8cc1" {4 keys}
		:message-id"0x5af50cad20e685a4867fbb65fe2ef765d20781889ab692d7e2245f5863436d8b"
		:chat-id"status"
		:public-key"0x04c7fcb5a2e5a01dc839b582186717c31a6b01d3c0f7790c938f268a0153dc1fcb69e04c2a8bd263f80c03fd27d74924819432e2552baa03afef2f3f67378c8cc1"
		:status:seen

// UNUSED?
:raw-payload-hash"0x8f79fe85be85574a5202b9cac915adbb1ad14ee7c386c13834384537eee62b85"


*
*/
type Message struct {
	ID          string `json:"message-id"`
	LegacyID    string `json:"old-message-id"`
	FromHex     string `json:"from"`
	ChatID      string `json:"chat-id"`
	ContentType string `json:"content-type"`
	MessageType string `json:"message-type"` // :public-group-user-message

	Content map[string]interface{} `json:"content"`

	Outgoing     bool `json:"outgoing"`       // true if our message, false otherwise
	LastOutgoing bool `json:"last-outgoing?"` // true if the last message in a group from us, we display status there like "Seen"

	DisplayPhoto    bool `json:"display-photo?"`    // true if not from the current user
	DisplayUsername bool `json:"display-username?"` // true if it is the first message FROM THE AUTHOR in the group

	// "Group" here means grouped messages by the same author
	FirstInGroup bool `json:"first-in-group?"` // true if the first one FROM THE AUTHOR
	LastInGroup  bool `json:"last-in-group?"`  // true if the last one FROM THE AUTHOR

	PayloadTimestamp int64  `json:"timestamp"` //nuff said
	WhisperTimestamp int64  `json:"whisper-timestamp"`
	TimestampString  string `json:"timestamp-str"`
	ClockValue       int64  `json:"clock-value"` // monotonic clock value
}

type Gap struct {
	From  int64 `json:"from"`
	To    int64 `json:"to"`
	Topic int64 `json:"topic"`
}

/*
:value"today"
:whisper-timestamp1559257145
:type:datemark
:display-photo?true
:last-in-group?true
:same-direction?false
:first-in-group?true
:last-outgoing?nil
:timestamp1559257143571
:display-username?true
*/
type Datemark struct {
	Date  time.Time `json:"date"`
	Value string    `json:"value"`
	Type  string    `json:"type"`
}

type MessagesAPIMixIn struct {
	chatMessages map[string][]ChatElement
	mu           sync.RWMutex
}

func (api *MessagesAPIMixIn) initMockMessages() {
	api.chatMessages = make(map[string][]ChatElement)
	for i := 0; i < 10; i++ {
		api.initMockMessageForChat(fmt.Sprintf("test%d", i))
	}
	api.initMockMessageForChat("status")

	fmt.Println("chatapi -> mock messages created")

	go api.insertNewMessages()
}

func (api *MessagesAPIMixIn) initMockMessageForChat(chatName string) {
	mockMessages := []ChatElement{}

	for i := 0; i < 1000; i++ {

		content := make(map[string]interface{})
		content["chat-id"] = chatName
		content["text"] = fmt.Sprintf("test-message-mock-%s-%d", chatName, i)

		msg := &Message{
			ID:               fmt.Sprintf("message-id-%s-%d", chatName, i),
			LegacyID:         fmt.Sprintf("message-id-legacy-%s-%d", chatName, i),
			FromHex:          "0x04c7fcb5a2e5a01dc839b582186717c31a6b01d3c0f7790c938f268a0153dc1fcb69e04c2a8bd263f80c03fd27d74924819432e2552baa03afef2f3f67378c8cc1",
			ChatID:           chatName,
			ContentType:      "text/plain",
			MessageType:      "public-group-user-message",
			Content:          content,
			Outgoing:         false,
			LastOutgoing:     false,
			DisplayPhoto:     true,
			DisplayUsername:  true,
			FirstInGroup:     true,
			LastInGroup:      true,
			PayloadTimestamp: time.Now().UnixNano(),
			WhisperTimestamp: time.Now().UnixNano(),
			TimestampString:  "1 millenia ago",
			ClockValue:       time.Now().UnixNano() + int64(i),
		}

		mockMessages = append(mockMessages, msg)

		if i%10 == 0 {
			// add a datemark
			datemarkValue := fmt.Sprintf("fake-datemark-%d", i/10)
			datemark := &Datemark{
				Value: datemarkValue,
				Type:  "datemark",
			}
			mockMessages = append(mockMessages, datemark)
		}

		if i%30 == 0 {
			// add a gap
		}
	}

	api.chatMessages[chatName] = mockMessages
}

func (api *MessagesAPIMixIn) GetMessagesBeforeClock(chatID string, clock string, limit int) ([]ChatElement, error) {
	api.mu.RLock()
	defer api.mu.RUnlock()
	msgs, _ := api.chatMessages[chatID]
	return msgs[:limit], nil
}

func (api *MessagesAPIMixIn) GetMessagesAfter(chatID string, clock string, limit int) ([]ChatElement, error) {
	api.mu.RLock()
	defer api.mu.RUnlock()
	msgs, _ := api.chatMessages[chatID]
	return msgs[:limit], nil
}
func (api *MessagesAPIMixIn) GetMessagesBetween(chatID string, clock1, clock2 string) ([]ChatElement, error) {
	api.mu.RLock()
	defer api.mu.RUnlock()
	msgs, _ := api.chatMessages[chatID]
	return msgs, nil
}

func (api *MessagesAPIMixIn) insertNewMessages() {
	for {
		time.Sleep(1 * time.Second)

		createdMessages := make(map[string][]*Message)

		for i, chatName := range []string{"status", "test1", "test3"} {
			content := make(map[string]interface{})
			content["chat-id"] = chatName
			content["text"] = fmt.Sprintf("test-message-mock-signal-%s-%v", chatName, time.Now().Unix())

			msg := &Message{
				ID:               fmt.Sprintf("message-id-%s-%v", chatName, time.Now().Unix()),
				LegacyID:         fmt.Sprintf("message-id-legacy-%s-%v", chatName, time.Now().Unix()),
				FromHex:          "0x04c7fcb5a2e5a01dc839b582186717c31a6b01d3c0f7790c938f268a0153dc1fcb69e04c2a8bd263f80c03fd27d74924819432e2552baa03afef2f3f67378c8cc1",
				ChatID:           chatName,
				ContentType:      "text/plain",
				MessageType:      "public-group-user-message",
				Content:          content,
				Outgoing:         false,
				LastOutgoing:     false,
				DisplayPhoto:     true,
				DisplayUsername:  true,
				FirstInGroup:     true,
				LastInGroup:      true,
				PayloadTimestamp: time.Now().UnixNano(),
				WhisperTimestamp: time.Now().UnixNano(),
				TimestampString:  "1 millenia ago",
				ClockValue:       time.Now().UnixNano() + int64(i),
			}

			api.mu.Lock()
			api.chatMessages[chatName] =
				append(api.chatMessages[chatName], msg)
			api.mu.Unlock()

			chatMessages, ok := createdMessages[chatName]
			if !ok {
				createdMessages[chatName] = []*Message{msg}
			} else {
				createdMessages[chatName] = append(chatMessages, msg)
			}
		}

		signal.SendNewMessagesSignal(createdMessages)
	}
}
