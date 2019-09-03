package mailserverrequestgaps

import "context"

func NewAPI(db *Database) *API {
	return &API{db}
}

// API is class with methods available over RPC.
type API struct {
	db *Database
}

func (a *API) AddMailserverRequestGaps(ctx context.Context, gaps []MailserverRequestGap) error {
	return a.db.Add(gaps)
}

func (a *API) GetMailserverRequestGaps(ctx context.Context, chatID string) ([]MailserverRequestGap, error) {
	return a.db.MailserverRequestGaps(chatID)
}

func (a *API) DeleteMailserverRequestGaps(ctx context.Context, ids []string) error {
	return a.db.Delete(ids)
}

func (a *API) DeleteMailserverRequestGapsByChatID(ctx context.Context, chatID string) error {
	return a.db.DeleteByChatID(chatID)
}
