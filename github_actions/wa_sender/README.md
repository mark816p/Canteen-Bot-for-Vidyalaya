# WhatsApp sender CLI

This folder contains the encrypted-session sender merged from `WhatsApp-Group-Notifier`.
It supports local `pair`, `compact`, `groups`, and group-only `send` commands.

- `send` accepts `WA_GROUP_JIDS` as a comma- or newline-separated list (or `WA_GROUP_JID` for a single group), so one run can target multiple groups over one connection.
- CI reconstructs the encrypted session from `WA_SESSION_ENC`, or from numbered variables (`WA_SESSION_ENC_PART001` through `WA_SESSION_ENC_PART005`, sorted and concatenated).
- Pairing is blocked in CI, and the session database is encrypted with AES-256-GCM at rest.

## Pair via QR code & export GitHub secret `.txt` files

Run the helper script locally from this folder (`github_actions/wa_sender`):

```powershell
python pair_and_export_secrets.py
```

The script will:
1. Generate a fresh 256-bit `WA_SESSION_KEY`.
2. Display the WhatsApp pairing QR code in your terminal (`WhatsApp -> Linked Devices`).
3. Compact the encrypted session so it fits inside GitHub's secret size limit.
4. Write ready-to-paste `.txt` files into `github_actions/wa_sender/secrets/` (gitignored):
   - `WA_SESSION_KEY.txt`
   - `WA_SESSION_ENC_PART001.txt` through `WA_SESSION_ENC_PART005.txt`
   - `joined_groups.txt` (all joined groups and their `@g.us` JIDs)
   - `WA_GROUP_JID_SAVYA.txt` and `WA_GROUP_JID_NAVRACHANA.txt` (when selected or matched)

Copy each `.txt` file's contents into the matching repository secret under **Settings -> Secrets and variables -> Actions**:
- `WA_SESSION_KEY`
- `WA_SESSION_ENC_PART001` .. `WA_SESSION_ENC_PART005`
- `WA_GROUP_JID_SAVYA` (used by `.github/workflows/test_notifier.yml` and `.github/workflows/daily_notifier.yml`)
- `WA_GROUP_JID_NAVRACHANA` (used by `.github/workflows/daily_notifier.yml`)

## Security & Testing

- Never commit plaintext/encrypted session files or the `secrets/` directory to this repository.
- Run the sender's unit tests with `go test ./...` from this folder.
