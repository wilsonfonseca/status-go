package mailserverrequestgaps

import (
	"database/sql"
	"fmt"
	"strings"
)

type MailserverRequestGap struct {
	ID     string `json:"id"`
	ChatID string `json:"chatId"`
	From   uint64 `json:"from"`
	To     uint64 `json:"to"`
}

// Database sql wrapper for operations with mailserver objects.
type Database struct {
	db *sql.DB
}

func NewDB(db *sql.DB) *Database {
	return &Database{db: db}
}

func (d *Database) Add(gaps []MailserverRequestGap) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			err = tx.Commit()
			return
		}
		_ = tx.Rollback()
	}()

	for _, gap := range gaps {

		_, err := tx.Exec(`INSERT OR REPLACE INTO mailserver_request_gaps(
				id,
				chat_id,
				gap_from,
				gap_to
			) VALUES (?, ?, ?, ?)`,
			gap.ID,
			gap.ChatID,
			gap.From,
			gap.To,
		)
		if err != nil {
			return err
		}

	}
	return nil
}

func (d *Database) MailserverRequestGaps(chatID string) ([]MailserverRequestGap, error) {
	var result []MailserverRequestGap

	rows, err := d.db.Query(`SELECT id, chat_id, gap_from, gap_to FROM mailserver_request_gaps WHERE chat_id = ?`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var m MailserverRequestGap
		if err := rows.Scan(
			&m.ID,
			&m.ChatID,
			&m.From,
			&m.To,
		); err != nil {
			return nil, err
		}
		result = append(result, m)
	}

	return result, nil
}

func (d *Database) Delete(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	inVector := strings.Repeat("?, ", len(ids)-1) + "?"
	query := fmt.Sprintf(`DELETE FROM mailserver_request_gaps WHERE id IN (%s)`, inVector) // nolint: gosec
	idsArgs := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		idsArgs = append(idsArgs, id)
	}

	_, err := d.db.Exec(query, idsArgs...)
	return err
}

func (d *Database) DeleteByChatID(chatID string) error {
	_, err := d.db.Exec(`DELETE FROM mailserver_request_gaps WHERE chat_id = ?`, chatID)
	return err
}
