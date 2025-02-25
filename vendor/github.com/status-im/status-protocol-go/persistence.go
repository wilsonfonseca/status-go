package statusproto

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/gob"
	"encoding/hex"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	"github.com/pkg/errors"

	protocol "github.com/status-im/status-protocol-go/v1"
)

const (
	uniqueIDContstraint = "UNIQUE constraint failed: user_messages.id"
)

var (
	// ErrMsgAlreadyExist returned if msg already exist.
	ErrMsgAlreadyExist = errors.New("message with given ID already exist")
)

// sqlitePersistence wrapper around sql db with operations common for a client.
type sqlitePersistence struct {
	db *sql.DB
}

func (db sqlitePersistence) LastMessageClock(chatID string) (int64, error) {
	if chatID == "" {
		return 0, errors.New("chat ID is empty")
	}

	var last sql.NullInt64
	err := db.db.QueryRow("SELECT max(clock) FROM user_messages WHERE chat_id = ?", chatID).Scan(&last)
	if err != nil {
		return 0, err
	}
	return last.Int64, nil
}

func (db sqlitePersistence) SaveChat(chat Chat) error {
	var err error

	pkey := []byte{}
	// For one to one chatID is an encoded public key
	if chat.ChatType == ChatTypeOneToOne {
		pkey, err = hex.DecodeString(chat.ID[2:])
		if err != nil {
			return err
		}
		// Safety check, make sure is well formed
		_, err := crypto.UnmarshalPubkey(pkey)
		if err != nil {
			return err
		}

	}

	// Encode members
	var encodedMembers bytes.Buffer
	memberEncoder := gob.NewEncoder(&encodedMembers)

	if err := memberEncoder.Encode(chat.Members); err != nil {
		return err
	}

	// Encode membership updates
	var encodedMembershipUpdates bytes.Buffer
	membershipUpdatesEncoder := gob.NewEncoder(&encodedMembershipUpdates)

	if err := membershipUpdatesEncoder.Encode(chat.MembershipUpdates); err != nil {
		return err
	}

	// Insert record
	stmt, err := db.db.Prepare(`INSERT INTO chats(id, name, color, active, type, timestamp,  deleted_at_clock_value, public_key, unviewed_message_count, last_clock_value, last_message_content_type, last_message_content, last_message_timestamp, members, membership_updates)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		chat.ID,
		chat.Name,
		chat.Color,
		chat.Active,
		chat.ChatType,
		chat.Timestamp,
		chat.DeletedAtClockValue,
		pkey,
		chat.UnviewedMessagesCount,
		chat.LastClockValue,
		chat.LastMessageContentType,
		chat.LastMessageContent,
		chat.LastMessageTimestamp,
		encodedMembers.Bytes(),
		encodedMembershipUpdates.Bytes(),
	)
	if err != nil {
		return err
	}

	return err
}

func (db sqlitePersistence) DeleteChat(chatID string) error {
	_, err := db.db.Exec("DELETE FROM chats WHERE id = ?", chatID)
	return err
}

func (db sqlitePersistence) Chats() ([]*Chat, error) {
	return db.chats(nil)
}

func (db sqlitePersistence) chats(tx *sql.Tx) ([]*Chat, error) {
	var err error

	if tx == nil {
		tx, err = db.db.BeginTx(context.Background(), &sql.TxOptions{})
		if err != nil {
			return nil, err
		}
		defer func() {
			if err == nil {
				err = tx.Commit()
				return

			}
			// don't shadow original error
			_ = tx.Rollback()
		}()
	}

	rows, err := tx.Query(`SELECT
		id,
		name,
		color,
		active,
		type,
		timestamp,
		deleted_at_clock_value,
		public_key,
		unviewed_message_count,
		last_clock_value,
		last_message_content_type,
		last_message_content,
		last_message_timestamp,
		members,
		membership_updates
	FROM chats
	ORDER BY chats.timestamp DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var response []*Chat

	for rows.Next() {
		var lastMessageContentType sql.NullString
		var lastMessageContent sql.NullString
		var lastMessageTimestamp sql.NullInt64

		chat := &Chat{}
		encodedMembers := []byte{}
		encodedMembershipUpdates := []byte{}
		pkey := []byte{}
		err := rows.Scan(
			&chat.ID,
			&chat.Name,
			&chat.Color,
			&chat.Active,
			&chat.ChatType,
			&chat.Timestamp,
			&chat.DeletedAtClockValue,
			&pkey,
			&chat.UnviewedMessagesCount,
			&chat.LastClockValue,
			&lastMessageContentType,
			&lastMessageContent,
			&lastMessageTimestamp,
			&encodedMembers,
			&encodedMembershipUpdates,
		)
		if err != nil {
			return nil, err
		}
		chat.LastMessageContent = lastMessageContent.String
		chat.LastMessageContentType = lastMessageContentType.String
		chat.LastMessageTimestamp = lastMessageTimestamp.Int64

		// Restore members
		membersDecoder := gob.NewDecoder(bytes.NewBuffer(encodedMembers))
		if err := membersDecoder.Decode(&chat.Members); err != nil {
			return nil, err
		}

		// Restore membership updates
		membershipUpdatesDecoder := gob.NewDecoder(bytes.NewBuffer(encodedMembershipUpdates))
		if err := membershipUpdatesDecoder.Decode(&chat.MembershipUpdates); err != nil {
			return nil, err
		}

		if len(pkey) != 0 {
			chat.PublicKey, err = crypto.UnmarshalPubkey(pkey)
			if err != nil {
				return nil, err
			}
		}
		response = append(response, chat)
	}

	return response, nil
}

func (db sqlitePersistence) Contacts() ([]*Contact, error) {
	rows, err := db.db.Query(`SELECT
	id,
	address,
	name,
	photo,
	last_updated,
	system_tags,
	device_info,
	tribute_to_talk
	FROM contacts`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var response []*Contact

	for rows.Next() {
		contact := &Contact{}
		encodedDeviceInfo := []byte{}
		encodedSystemTags := []byte{}
		err := rows.Scan(
			&contact.ID,
			&contact.Address,
			&contact.Name,
			&contact.Photo,
			&contact.LastUpdated,
			&encodedSystemTags,
			&encodedDeviceInfo,
			&contact.TributeToTalk,
		)
		if err != nil {
			return nil, err
		}

		// Restore device info
		deviceInfoDecoder := gob.NewDecoder(bytes.NewBuffer(encodedDeviceInfo))
		if err := deviceInfoDecoder.Decode(&contact.DeviceInfo); err != nil {
			return nil, err
		}

		// Restore system tags
		systemTagsDecoder := gob.NewDecoder(bytes.NewBuffer(encodedSystemTags))
		if err := systemTagsDecoder.Decode(&contact.SystemTags); err != nil {
			return nil, err
		}

		response = append(response, contact)
	}

	return response, nil
}

func (db sqlitePersistence) SaveContact(contact Contact, tx *sql.Tx) error {
	var err error

	if tx == nil {
		tx, err = db.db.BeginTx(context.Background(), &sql.TxOptions{})
		if err != nil {
			return err
		}
		defer func() {
			if err == nil {
				err = tx.Commit()
				return

			}
			// don't shadow original error
			_ = tx.Rollback()
		}()
	}

	// Encode device info
	var encodedDeviceInfo bytes.Buffer
	deviceInfoEncoder := gob.NewEncoder(&encodedDeviceInfo)

	if err := deviceInfoEncoder.Encode(contact.DeviceInfo); err != nil {
		return err
	}

	// Encoded system tags
	var encodedSystemTags bytes.Buffer
	systemTagsEncoder := gob.NewEncoder(&encodedSystemTags)

	if err := systemTagsEncoder.Encode(contact.SystemTags); err != nil {
		return err
	}

	// Insert record
	stmt, err := tx.Prepare(`INSERT INTO contacts(
	  id,
	  address,
	  name,
	  photo,
	  last_updated,
	  system_tags,
	  device_info,
	  tribute_to_talk
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		contact.ID,
		contact.Address,
		contact.Name,
		contact.Photo,
		contact.LastUpdated,
		encodedSystemTags.Bytes(),
		encodedDeviceInfo.Bytes(),
		contact.TributeToTalk,
	)
	return err
}

// Messages returns messages for a given contact, in a given period. Ordered by a timestamp.
func (db sqlitePersistence) Messages(from, to time.Time) (result []*protocol.Message, err error) {
	rows, err := db.db.Query(`SELECT
			id, 
			content_type, 
			message_type, 
			text, 
			clock, 
			timestamp, 
			content_chat_id, 
			content_text, 
			public_key, 
			flags
		FROM user_messages 
		WHERE timestamp >= ? AND timestamp <= ? 
		ORDER BY timestamp`,
		protocol.TimestampInMsFromTime(from),
		protocol.TimestampInMsFromTime(to),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rst []*protocol.Message
	for rows.Next() {
		msg := protocol.Message{
			Content: protocol.Content{},
		}
		pkey := []byte{}
		err = rows.Scan(
			&msg.ID, &msg.ContentT, &msg.MessageT, &msg.Text, &msg.Clock,
			&msg.Timestamp, &msg.Content.ChatID, &msg.Content.Text, &pkey, &msg.Flags)
		if err != nil {
			return nil, err
		}
		if len(pkey) != 0 {
			msg.SigPubKey, err = unmarshalECDSAPub(pkey)
			if err != nil {
				return nil, err
			}
		}
		rst = append(rst, &msg)
	}
	return rst, nil
}

func (db sqlitePersistence) NewMessages(chatID string, rowid int64) ([]*protocol.Message, error) {
	rows, err := db.db.Query(`SELECT
id, content_type, message_type, text, clock, timestamp, content_chat_id, content_text, public_key, flags
FROM user_messages WHERE chat_id = ? AND rowid >= ? ORDER BY clock`,
		chatID, rowid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		rst = []*protocol.Message{}
	)
	for rows.Next() {
		msg := protocol.Message{
			Content: protocol.Content{},
		}
		pkey := []byte{}
		err = rows.Scan(
			&msg.ID, &msg.ContentT, &msg.MessageT, &msg.Text, &msg.Clock,
			&msg.Timestamp, &msg.Content.ChatID, &msg.Content.Text, &pkey, &msg.Flags)
		if err != nil {
			return nil, err
		}
		if len(pkey) != 0 {
			msg.SigPubKey, err = unmarshalECDSAPub(pkey)
			if err != nil {
				return nil, err
			}
		}
		rst = append(rst, &msg)
	}
	return rst, nil
}

// TODO(adam): refactor all message getters in order not to
// repeat the select fields over and over.
func (db sqlitePersistence) UnreadMessages(chatID string) ([]*protocol.Message, error) {
	rows, err := db.db.Query(`
		SELECT
			id,
			content_type,
			message_type,
			text,
			clock,
			timestamp,
			content_chat_id,
			content_text,
			public_key,
			flags
		FROM
			user_messages
		WHERE
			chat_id = ? AND
			flags & ? == 0
		ORDER BY clock`,
		chatID, protocol.MessageRead,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*protocol.Message

	for rows.Next() {
		msg := protocol.Message{
			Content: protocol.Content{},
		}
		pkey := []byte{}
		err = rows.Scan(
			&msg.ID, &msg.ContentT, &msg.MessageT, &msg.Text, &msg.Clock,
			&msg.Timestamp, &msg.Content.ChatID, &msg.Content.Text, &pkey, &msg.Flags)
		if err != nil {
			return nil, err
		}
		if len(pkey) != 0 {
			msg.SigPubKey, err = unmarshalECDSAPub(pkey)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, &msg)
	}

	return result, nil
}

func (db sqlitePersistence) SaveMessages(messages []*protocol.Message) (last int64, err error) {
	var (
		tx   *sql.Tx
		stmt *sql.Stmt
	)
	tx, err = db.db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		return
	}
	stmt, err = tx.Prepare(`INSERT INTO user_messages(
id, chat_id, content_type, message_type, text, clock, timestamp, content_chat_id, content_text, public_key, flags)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return
	}
	defer func() {
		if err == nil {
			err = tx.Commit()
			return

		}
		// don't shadow original error
		_ = tx.Rollback()
	}()

	var rst sql.Result

	for _, msg := range messages {
		pkey := []byte{}
		if msg.SigPubKey != nil {
			pkey, err = marshalECDSAPub(msg.SigPubKey)
		}
		rst, err = stmt.Exec(
			msg.ID, msg.ChatID, msg.ContentT, msg.MessageT, msg.Text,
			msg.Clock, msg.Timestamp, msg.Content.ChatID, msg.Content.Text,
			pkey, msg.Flags)
		if err != nil {
			if err.Error() == uniqueIDContstraint {
				// skip duplicated messages
				err = nil
				continue
			}
			return
		}

		last, err = rst.LastInsertId()
		if err != nil {
			return
		}
	}
	return
}

func marshalECDSAPub(pub *ecdsa.PublicKey) (rst []byte, err error) {
	switch pub.Curve.(type) {
	case *secp256k1.BitCurve:
		rst = make([]byte, 34)
		rst[0] = 1
		copy(rst[1:], secp256k1.CompressPubkey(pub.X, pub.Y))
		return rst[:], nil
	default:
		return nil, errors.New("unknown curve")
	}
}

func unmarshalECDSAPub(buf []byte) (*ecdsa.PublicKey, error) {
	pub := &ecdsa.PublicKey{}
	if len(buf) < 1 {
		return nil, errors.New("too small")
	}
	switch buf[0] {
	case 1:
		pub.Curve = secp256k1.S256()
		pub.X, pub.Y = secp256k1.DecompressPubkey(buf[1:])
		ok := pub.IsOnCurve(pub.X, pub.Y)
		if !ok {
			return nil, errors.New("not on curve")
		}
		return pub, nil
	default:
		return nil, errors.New("unknown curve")
	}
}
