# WhatsApp sender CLI

This folder contains the encrypted-session sender merged from `WhatsApp-Group-Notifier`.
It supports local `pair` and `groups` commands and a group-only `send` command. `send` accepts
`WA_GROUP_JIDS` as a comma- or newline-separated list, so one run can target multiple groups.
Pairing is
blocked in CI, and the session database is encrypted with AES-256-GCM at rest.

The canteen workflows currently run the scraper without WhatsApp. They must not restore or
reuse the older WhatsApp session. Do not pair or send with an account that may be compromised,
and never commit plaintext or encrypted session files to this public repository.

Run the sender's unit tests with `go test ./...` from this folder. Use a private repository and
a newly paired account before configuring any future Actions workflow to send messages.
