Mailserver request gaps Service
================

Mailserver request gaps service provides read/write API for `MailserverRequestGap` object 
which stores details about the gaps between mailserver requests.

API
---

The service exposes four methods

#### mailserverrequestgaps_addMailserverRequestGaps

Stores `MailserverRequestGap` in the database.
All fields are specified below:

```json
{
  "id": "1",
  "chatId": "chat-id",
  "from": 1,
  "to": 2
}
```

#### mailserverrequestgaps_getMailserverRequestGaps

Reads all saved mailserver request gaps by chatID.

#### mailserverrequestgaps_deleteMailserverRequestGaps

Deletes all MailserverRequestGaps specified by IDs.

#### mailserverrequestgaps_deleteMailserverRequestGapsByChatID

Deletes all MailserverRequestGaps specified by chatID.
