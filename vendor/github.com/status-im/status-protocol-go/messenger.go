package statusproto

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
	whisper "github.com/status-im/whisper/whisperv6"
	"go.uber.org/zap"

	"github.com/status-im/status-protocol-go/encryption"
	"github.com/status-im/status-protocol-go/encryption/multidevice"
	"github.com/status-im/status-protocol-go/encryption/sharedsecret"
	"github.com/status-im/status-protocol-go/sqlite"
	transport "github.com/status-im/status-protocol-go/transport/whisper"
	protocol "github.com/status-im/status-protocol-go/v1"
)

var (
	ErrChatIDEmpty    = errors.New("chat ID is empty")
	ErrNotImplemented = errors.New("not implemented")
)

// Messenger is a entity managing chats and messages.
// It acts as a bridge between the application and encryption
// layers.
// It needs to expose an interface to manage installations
// because installations are managed by the user.
// Similarly, it needs to expose an interface to manage
// mailservers because they can also be managed by the user.
type Messenger struct {
	identity    *ecdsa.PrivateKey
	persistence *sqlitePersistence
	transport   *transport.WhisperServiceTransport
	encryptor   *encryption.Protocol
	processor   *messageProcessor
	logger      *zap.Logger

	ownMessages                []*protocol.Message
	featureFlags               featureFlags
	messagesPersistenceEnabled bool
	shutdownTasks              []func() error
}

type featureFlags struct {
	genericDiscoveryTopicEnabled bool
	// sendV1Messages indicates whether we should send
	// messages compatible only with V1 and later.
	// V1 messages adds additional wrapping
	// which contains a signature and payload.
	sendV1Messages bool

	// datasync indicates whether direct messages should be sent exclusively
	// using datasync, breaking change for non-v1 clients. Public messages
	// are not impacted
	datasync bool
}

type dbConfig struct {
	dbPath string
	dbKey  string
}

type config struct {
	onNewInstallationsHandler func([]*multidevice.Installation)
	// This needs to be exposed until we move here mailserver logic
	// as otherwise the client is not notified of a new filter and
	// won't be pulling messages from mailservers until it reloads the chats/filters
	onNegotiatedFilters func([]*transport.Filter)
	// DEPRECATED: no need to expose it
	onSendContactCodeHandler func(*encryption.ProtocolMessageSpec)

	// Config for the envelopes monitor
	envelopesMonitorConfig *transport.EnvelopesMonitorConfig

	messagesPersistenceEnabled bool
	featureFlags               featureFlags

	// A path to a database or a database instance is required.
	// The database instance has a higher priority.
	dbConfig dbConfig
	db       *sql.DB

	logger *zap.Logger
}

type Option func(*config) error

func WithOnNewInstallationsHandler(h func([]*multidevice.Installation)) Option {
	return func(c *config) error {
		c.onNewInstallationsHandler = h
		return nil
	}
}

func WithOnNegotiatedFilters(h func([]*transport.Filter)) Option {
	return func(c *config) error {
		c.onNegotiatedFilters = h
		return nil
	}
}

func WithCustomLogger(logger *zap.Logger) Option {
	return func(c *config) error {
		c.logger = logger
		return nil
	}
}

func WithGenericDiscoveryTopicSupport() Option {
	return func(c *config) error {
		c.featureFlags.genericDiscoveryTopicEnabled = true
		return nil
	}
}

func WithMessagesPersistenceEnabled() Option {
	return func(c *config) error {
		c.messagesPersistenceEnabled = true
		return nil
	}
}

func WithDatabaseConfig(dbPath, dbKey string) Option {
	return func(c *config) error {
		c.dbConfig = dbConfig{dbPath: dbPath, dbKey: dbKey}
		return nil
	}
}

func WithDatabase(db *sql.DB) Option {
	return func(c *config) error {
		c.db = db
		return nil
	}
}

func WithSendV1Messages() Option {
	return func(c *config) error {
		c.featureFlags.sendV1Messages = true
		return nil
	}
}

func WithDatasync() func(c *config) error {
	return func(c *config) error {
		c.featureFlags.datasync = true
		return nil
	}
}

func WithEnvelopesMonitorConfig(emc *transport.EnvelopesMonitorConfig) Option {
	return func(c *config) error {
		c.envelopesMonitorConfig = emc
		return nil
	}
}

func NewMessenger(
	identity *ecdsa.PrivateKey,
	shh *whisper.Whisper,
	installationID string,
	opts ...Option,
) (*Messenger, error) {
	var messenger *Messenger

	c := config{}

	for _, opt := range opts {
		if err := opt(&c); err != nil {
			return nil, err
		}
	}

	logger := c.logger
	if c.logger == nil {
		var err error
		if logger, err = zap.NewDevelopment(); err != nil {
			return nil, errors.Wrap(err, "failed to create a logger")
		}
	}

	// Set default config fields.
	if c.onNewInstallationsHandler == nil {
		c.onNewInstallationsHandler = func(installations []*multidevice.Installation) {
			sugar := logger.Sugar().With("site", "onNewInstallationsHandler")
			for _, installation := range installations {
				sugar.Infow(
					"received a new installation",
					"identity", installation.Identity,
					"id", installation.ID)
			}
		}
	}
	onNewSharedSecretHandler := func(secrets []*sharedsecret.Secret) {
		filters, err := messenger.handleSharedSecrets(secrets)
		if err != nil {
			slogger := logger.With(zap.String("site", "onNewSharedSecretHandler"))
			slogger.Warn("failed to process secrets", zap.Error(err))
		}

		if c.onNegotiatedFilters != nil {
			c.onNegotiatedFilters(filters)
		}
	}
	if c.onSendContactCodeHandler == nil {
		c.onSendContactCodeHandler = func(messageSpec *encryption.ProtocolMessageSpec) {
			slogger := logger.With(zap.String("site", "onSendContactCodeHandler"))
			slogger.Info("received a SendContactCode request")

			newMessage, err := messageSpecToWhisper(messageSpec)
			if err != nil {
				slogger.Warn("failed to convert spec to Whisper message", zap.Error(err))
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			chatName := transport.ContactCodeTopic(&messenger.identity.PublicKey)
			_, err = messenger.transport.SendPublic(ctx, &newMessage, chatName)
			if err != nil {
				slogger.Warn("failed to send a contact code", zap.Error(err))
			}
		}
	}

	// Configure the database.
	database := c.db
	if c.db == nil && c.dbConfig == (dbConfig{}) {
		return nil, errors.New("database instance or database path needs to be provided")
	}
	if c.db == nil {
		logger.Info("opening a database", zap.String("dbPath", c.dbConfig.dbPath))
		var err error
		database, err = sqlite.Open(c.dbConfig.dbPath, c.dbConfig.dbKey)
		if err != nil {
			return nil, errors.Wrap(err, "failed to initialize database from the db config")
		}
	}

	// Apply migrations for all components.
	err := sqlite.Migrate(database)
	if err != nil {
		return nil, errors.Wrap(err, "failed to apply migrations")
	}

	// Initialize transport layer.
	t, err := transport.NewWhisperServiceTransport(
		shh,
		identity,
		database,
		nil,
		c.envelopesMonitorConfig,
		logger,
		transport.SetGenericDiscoveryTopicSupport(c.featureFlags.genericDiscoveryTopicEnabled),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create a WhisperServiceTransport")
	}

	// Initialize encryption layer.
	encryptionProtocol := encryption.New(
		database,
		installationID,
		c.onNewInstallationsHandler,
		onNewSharedSecretHandler,
		c.onSendContactCodeHandler,
		logger,
	)

	processor, err := newMessageProcessor(
		identity,
		database,
		encryptionProtocol,
		t,
		logger,
		c.featureFlags,
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create messageProcessor")
	}

	messenger = &Messenger{
		identity:                   identity,
		persistence:                &sqlitePersistence{db: database},
		transport:                  t,
		encryptor:                  encryptionProtocol,
		processor:                  processor,
		featureFlags:               c.featureFlags,
		messagesPersistenceEnabled: c.messagesPersistenceEnabled,
		shutdownTasks: []func() error{
			database.Close,
			t.Reset,
			t.Stop,
			func() error { processor.Stop(); return nil },
			// Currently this often fails, seems like it's safe to ignore them
			// https://github.com/uber-go/zap/issues/328
			func() error { _ = logger.Sync; return nil },
		},
		logger: logger,
	}

	// Start all services immediately.
	// TODO: consider removing identity as an argument to Start().
	if err := encryptionProtocol.Start(identity); err != nil {
		return nil, err
	}

	logger.Debug("messages persistence", zap.Bool("enabled", c.messagesPersistenceEnabled))

	return messenger, nil
}

// Init analyzes chats and contacts in order to setup filters
// which are responsible for retrieving messages.
func (m *Messenger) Init() error {
	logger := m.logger.With(zap.String("site", "Init"))

	var (
		publicChatIDs []string
		publicKeys    []*ecdsa.PublicKey
	)

	// Get chat IDs and public keys from the existing chats.
	// TODO: Get only active chats by the query.
	chats, err := m.Chats()
	if err != nil {
		return err
	}
	for _, chat := range chats {
		if !chat.Active {
			continue
		}
		switch chat.ChatType {
		case ChatTypePublic:
			publicChatIDs = append(publicChatIDs, chat.ID)
		case ChatTypeOneToOne:
			publicKeys = append(publicKeys, chat.PublicKey)
		case ChatTypePrivateGroupChat:
			for _, member := range chat.Members {
				publicKey, err := member.PublicKey()
				if err != nil {
					return errors.Wrapf(err, "invalid public key for member %s in chat %s", member.ID, chat.Name)
				}
				publicKeys = append(publicKeys, publicKey)
			}
		default:
			return errors.New("invalid chat type")
		}
	}

	// Get chat IDs and public keys from the contacts.
	contacts, err := m.Contacts()
	if err != nil {
		return err
	}
	for _, contact := range contacts {
		// We only need filters for contacts added by us and not blocked.
		if !contact.IsAdded() || contact.IsBlocked() {
			continue
		}
		publicKey, err := contact.PublicKey()
		if err != nil {
			logger.Error("failed to get contact's public key", zap.Error(err))
			continue
		}
		publicKeys = append(publicKeys, publicKey)
	}

	_, err = m.transport.InitFilters(publicChatIDs, publicKeys)
	return err
}

// Shutdown takes care of ensuring a clean shutdown of Messenger
func (m *Messenger) Shutdown() (err error) {
	for _, task := range m.shutdownTasks {
		if tErr := task(); tErr != nil {
			if err == nil {
				// First error appeared.
				err = tErr
			} else {
				// We return all errors. They will be concatenated in the order of occurrence,
				// however, they will also be returned as a single error.
				err = errors.Wrap(err, tErr.Error())
			}
		}
	}
	return
}

func (m *Messenger) handleSharedSecrets(secrets []*sharedsecret.Secret) ([]*transport.Filter, error) {
	logger := m.logger.With(zap.String("site", "handleSharedSecrets"))
	var result []*transport.Filter
	for _, secret := range secrets {
		logger.Debug("received shared secret", zap.Binary("identity", crypto.FromECDSAPub(secret.Identity)))
		fSecret := transport.NegotiatedSecret{
			PublicKey: secret.Identity,
			Key:       secret.Key,
		}
		filter, err := m.transport.ProcessNegotiatedSecret(fSecret)
		if err != nil {
			return nil, err
		}
		result = append(result, filter)
	}
	return result, nil
}

func (m *Messenger) EnableInstallation(id string) error {
	return m.encryptor.EnableInstallation(&m.identity.PublicKey, id)
}

func (m *Messenger) DisableInstallation(id string) error {
	return m.encryptor.DisableInstallation(&m.identity.PublicKey, id)
}

func (m *Messenger) Installations() ([]*multidevice.Installation, error) {
	return m.encryptor.GetOurInstallations(&m.identity.PublicKey)
}

func (m *Messenger) SetInstallationMetadata(id string, data *multidevice.InstallationMetadata) error {
	return m.encryptor.SetInstallationMetadata(&m.identity.PublicKey, id, data)
}

// NOT IMPLEMENTED
func (m *Messenger) SelectMailserver(id string) error {
	return ErrNotImplemented
}

// NOT IMPLEMENTED
func (m *Messenger) AddMailserver(enode string) error {
	return ErrNotImplemented
}

// NOT IMPLEMENTED
func (m *Messenger) RemoveMailserver(id string) error {
	return ErrNotImplemented
}

// NOT IMPLEMENTED
func (m *Messenger) Mailservers() ([]string, error) {
	return nil, ErrNotImplemented
}

func (m *Messenger) Join(chat Chat) error {
	if chat.PublicKey != nil {
		return m.transport.JoinPrivate(chat.PublicKey)
	} else if chat.Name != "" {
		return m.transport.JoinPublic(chat.Name)
	}
	return errors.New("chat is neither public nor private")
}

func (m *Messenger) Leave(chat Chat) error {
	if chat.PublicKey != nil {
		return m.transport.LeavePrivate(chat.PublicKey)
	} else if chat.Name != "" {
		return m.transport.LeavePublic(chat.Name)
	}
	return errors.New("chat is neither public nor private")
}

func (m *Messenger) SaveChat(chat Chat) error {
	return m.persistence.SaveChat(chat)
}

func (m *Messenger) Chats() ([]*Chat, error) {
	return m.persistence.Chats()
}

func (m *Messenger) DeleteChat(chatID string) error {
	return m.persistence.DeleteChat(chatID)
}

func (m *Messenger) SaveContact(contact Contact) error {
	return m.persistence.SaveContact(contact, nil)
}

func (m *Messenger) BlockContact(contact Contact) ([]*Chat, error) {
	return m.persistence.BlockContact(contact)
}

func (m *Messenger) Contacts() ([]*Contact, error) {
	return m.persistence.Contacts()
}

func (m *Messenger) Send(ctx context.Context, chat Chat, data []byte) ([]byte, error) {
	logger := m.logger.With(zap.String("site", "Send"), zap.String("chatID", chat.ID))

	chatID := chat.ID
	if chatID == "" {
		return nil, ErrChatIDEmpty
	}

	clock, err := m.persistence.LastMessageClock(chat.ID)
	if err != nil {
		return nil, err
	}

	logger.Debug("last message clock received", zap.Int64("clock", clock))

	if chat.PublicKey != nil {
		logger.Debug("sending private message", zap.Binary("publicKey", crypto.FromECDSAPub(chat.PublicKey)))

		hash, message, err := m.processor.SendPrivate(ctx, chat.PublicKey, chat.ID, data, clock)
		if err != nil {
			return nil, err
		}

		// Save our message because it won't be received from the transport layer.
		message.ID = hash // a Message need ID to be properly stored in the db
		message.SigPubKey = &m.identity.PublicKey

		if m.messagesPersistenceEnabled {
			_, err = m.persistence.SaveMessages([]*protocol.Message{message})
			if err != nil {
				return nil, err
			}
		}

		// Cache it to be returned in Retrieve().
		m.ownMessages = append(m.ownMessages, message)

		return hash, nil
	} else if chat.Name != "" {
		logger.Debug("sending public message", zap.String("chatName", chat.Name))
		return m.processor.SendPublic(ctx, chat.Name, chat.ID, data, clock)
	}
	return nil, errors.New("chat is neither public nor private")
}

// SendRaw takes encoded data, encrypts it and sends through the wire.
// DEPRECATED
func (m *Messenger) SendRaw(ctx context.Context, chat Chat, data []byte) ([]byte, error) {
	if chat.PublicKey != nil {
		return m.processor.SendPrivateRaw(ctx, chat.PublicKey, data)
	} else if chat.Name != "" {
		return m.processor.SendPublicRaw(ctx, chat.Name, data)
	}
	return nil, errors.New("chat is neither public nor private")
}

type RetrieveConfig struct {
	From        time.Time
	To          time.Time
	latest      bool
	last24Hours bool
}

var (
	RetrieveLatest  = RetrieveConfig{latest: true}
	RetrieveLastDay = RetrieveConfig{latest: true, last24Hours: true}
)

// RetrieveAll retrieves all previously fetched messages
func (m *Messenger) RetrieveAll(ctx context.Context, c RetrieveConfig) ([]*protocol.Message, error) {
	latest, err := m.transport.RetrieveAllMessages()
	if err != nil {
		return nil, errors.Wrap(err, "failed to retrieve messages")
	}

	logger := m.logger.With(zap.String("site", "RetrieveAll"))
	logger.Debug("retrieved messages grouped by chat", zap.Int("count", len(latest)))

	var result []*protocol.Message

	for _, chat := range latest {
		logger.Debug("processing chat", zap.String("chatID", chat.ChatID))
		protoMessages, err := m.processor.Process(chat.Messages)
		if err != nil {
			return nil, err
		}
		result = append(result, protoMessages...)
	}

	_, err = m.persistence.SaveMessages(result)
	if err != nil {
		return nil, errors.Wrap(err, "failed to save messages")
	}

	retrievedMessages, err := m.retrieveSaved(ctx, c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get saved messages")
	}
	result = append(result, retrievedMessages...)

	// Include own messages.
	result = append(result, m.ownMessages...)
	m.ownMessages = nil

	return result, nil
}

func (m *Messenger) retrieveSaved(ctx context.Context, c RetrieveConfig) (messages []*protocol.Message, err error) {
	if !m.messagesPersistenceEnabled {
		return nil, nil
	}

	if !c.latest {
		return m.persistence.Messages(c.From, c.To)
	}

	if c.last24Hours {
		to := time.Now()
		from := to.Add(-time.Hour * 24)
		return m.persistence.Messages(from, to)
	}

	return nil, nil
}

// DEPRECATED
func (m *Messenger) RetrieveRawAll() (map[transport.Filter][]*protocol.StatusMessage, error) {
	chatWithMessages, err := m.transport.RetrieveRawAll()
	if err != nil {
		return nil, err
	}

	logger := m.logger.With(zap.String("site", "RetrieveRawAll"))
	result := make(map[transport.Filter][]*protocol.StatusMessage)

	for chat, messages := range chatWithMessages {
		for _, message := range messages {
			shhMessage := whisper.ToWhisperMessage(message)
			// TODO: fix this to use an exported method.
			statusMessages, err := m.processor.handleMessages(shhMessage, false)
			if err != nil {
				logger.Info("failed to decode messages", zap.Error(err))
				continue
			}
			result[chat] = append(result[chat], statusMessages...)
		}
	}

	return result, nil
}

// DEPRECATED
func (m *Messenger) LoadFilters(filters []*transport.Filter) ([]*transport.Filter, error) {
	return m.transport.LoadFilters(filters)
}

// DEPRECATED
func (m *Messenger) RemoveFilters(filters []*transport.Filter) error {
	return m.transport.RemoveFilters(filters)
}

// DEPRECATED
func (m *Messenger) ConfirmMessagesProcessed(messageIDs [][]byte) error {
	for _, id := range messageIDs {
		if err := m.encryptor.ConfirmMessageProcessed(id); err != nil {
			return err
		}
	}
	return nil
}

// DEPRECATED: required by status-react.
func (m *Messenger) MessageByID(id string) (*Message, error) {
	return m.persistence.MessageByID(id)
}

// DEPRECATED: required by status-react.
func (m *Messenger) MessagesExist(ids []string) (map[string]bool, error) {
	return m.persistence.MessagesExist(ids)
}

// DEPRECATED: required by status-react.
func (m *Messenger) MessageByChatID(chatID, cursor string, limit int) ([]*Message, string, error) {
	return m.persistence.MessageByChatID(chatID, cursor, limit)
}

// DEPRECATED: required by status-react.
func (m *Messenger) SaveMessages(messages []*Message) error {
	return m.persistence.SaveMessagesLegacy(messages)
}

// DEPRECATED: required by status-react.
func (m *Messenger) DeleteMessage(id string) error {
	return m.persistence.DeleteMessage(id)
}

// DEPRECATED: required by status-react.
func (m *Messenger) DeleteMessagesByChatID(id string) error {
	return m.persistence.DeleteMessagesByChatID(id)
}

// DEPRECATED: required by status-react.
func (m *Messenger) MarkMessagesSeen(ids ...string) error {
	return m.persistence.MarkMessagesSeen(ids...)
}

// DEPRECATED: required by status-react.
func (m *Messenger) UpdateMessageOutgoingStatus(id, newOutgoingStatus string) error {
	return m.persistence.UpdateMessageOutgoingStatus(id, newOutgoingStatus)
}
