# Canteen Bot for Vidyalaya

This project automates the retrieval of the Vidyalaya Canteen Menu and sends the daily menu updates to designated WhatsApp groups using Edge Selenium scraping and GoWa for WhatsApp messaging.

## Features
- **Automated Menu Scraping**: Logs into the Vidyalaya student portal and scrapes the lunch menu for the next day using a headless Edge browser.
- **WhatsApp Notification**: Sends the formatted menu directly to your configured WhatsApp groups.
- **Scheduled Execution**: Includes an `autorun.bat` script that can be used with Windows Task Scheduler to run the script automatically every day.
- **Battery & Network Resilient**: Configured to run on battery power and wake the computer if needed.

## Setup

1. **Prerequisites**:
   - Python 3 installed.
   - [GoWA](https://github.com/dimaskiddo/go-whatsapp-web-multidevice) (or a compatible WhatsApp Web multidevice API) running locally on port 3000.
   - Microsoft Edge installed.

2. **Configuration**:
   - Copy `nss/.env.example` to `nss/.env` and update your Vidyalaya portal credentials.
   - Configure your WhatsApp group JIDs (e.g., `120363XXXXXXXXXXXX@g.us`) in the `.env` file under `WHATSAPP_GROUP_NAMES`.

3. **Running the Script**:
   - To run the script manually, execute `autorun.bat` from the project root. This will automatically set up the Python virtual environment, install dependencies, and run the script.
   
4. **Task Scheduling**:
   - You can schedule `autorun.bat` to run daily via Windows Task Scheduler.

## Project Structure
- `autorun.bat`: The entry point script that handles environment setup, execution, and logging.
- `nss/main.py`: The core Python logic for scraping and sending messages locally.
- `nss/.env`: Configuration file (create from `.env.example`).
- `drivers/`: Contains the Microsoft Edge WebDriver used by Selenium.
- `github_actions/scraper.py`: GitHub Actions headless Chrome menu scraper.
- `github_actions/wa_sender/`: Encrypted-session Go WhatsApp sender and QR pairing helper.
- `.github/workflows/test_notifier.yml`: Manual Savya-only test workflow (`WA_GROUP_JID_SAVYA`).
- `.github/workflows/daily_notifier.yml`: Daily workflow sending to both groups (`WA_GROUP_JID_NAVRACHANA` and `WA_GROUP_JID_SAVYA`).

## GitHub Actions Setup & WhatsApp Pairing

To pair a WhatsApp account and generate `.txt` files for all required WhatsApp GitHub Secrets (`WA_SESSION_KEY`, `WA_SESSION_ENC_PART001`–`005`, `WA_GROUP_JID_SAVYA`, and `WA_GROUP_JID_NAVRACHANA`), run:

```powershell
cd github_actions/wa_sender
python pair_and_export_secrets.py
```

This displays a QR code in your terminal and writes gitignored `.txt` files into `github_actions/wa_sender/secrets/`. See [github_actions/wa_sender/README.md](github_actions/wa_sender/README.md) for full details.
