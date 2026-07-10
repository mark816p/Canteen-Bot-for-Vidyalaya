# Canteen Meal Notifier Vidyalaya

Scrapes the Vidyalaya canteen menu with Selenium in Microsoft Edge, stores menu rows in SQLite, and sends tomorrow's meal to WhatsApp through `go-whatsapp-web-multidevice`.

## What changed

- Browser changed from Firefox to Microsoft Edge.
- Vidyalaya canteen default changed from `NSS-CANTEEN` to `NHSS-CANTEEN`.
- The break dropdown now tolerates the updated `- ALL -` option.
- Scraping is more resilient to small table/layout changes.
- Discord webhooks were removed from the runtime path.
- WhatsApp is sent with `POST /send/message`.
- The default destination is the WhatsApp group named `Navrachana Sama Canteen`; the script resolves its group JID from GoWA before sending.
- Database writes now upsert, so rerunning the scraper does not fail on duplicate dates.

## Setup

1. Copy `.env.example` to `.env` and adjust values if needed.
2. Start `go-whatsapp-web-multidevice` in REST mode on port `3000`.
3. Log in to WhatsApp from the GoWA web UI or pairing flow.
4. Run:

```bat
..\autorun.bat --dry-run --refresh
```

Remove `--dry-run` only after the printed message looks correct and GoWA is logged in.

## Important settings

```env
VIDYALAYA_USERNAME=20024B
VIDYALAYA_PASSWORD=784781
VIDYALAYA_CANTEEN=NHSS-CANTEEN
VIDYALAYA_BREAK=Lunch Break

WHATSAPP_API_URL=http://localhost:3000/send/message
WHATSAPP_GROUP_NAME=Navrachana Sama Canteen
```

For GoWA v8 with multiple devices, set `WHATSAPP_DEVICE_ID`. If GoWA uses basic auth, set `WHATSAPP_BASIC_AUTH=user:password`.

If group-name lookup ever fails, set `WHATSAPP_PHONE` to the exact group JID ending in `@g.us`.

## Verification run

Verified on July 9, 2026 with:

```powershell
python main.py --dry-run --refresh --target-date 2026-07-10
```

The script logged in with Edge, scraped 31 menu records, and produced:

```text
Date: 2026-07-10
Food:
1) Manchurian Noodles
2) Sweet Corn Soup
3) Fruit
```

WhatsApp delivery was not live-tested because no GoWA server was running on `localhost:3000` in this environment.
