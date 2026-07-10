from __future__ import annotations

import argparse
import os
import re
import sqlite3
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path
from urllib.parse import urljoin
from typing import Any, Iterable

import requests
from selenium import webdriver
from selenium.common.exceptions import StaleElementReferenceException, TimeoutException
from selenium.webdriver.common.by import By
from selenium.webdriver.edge.options import Options as EdgeOptions
from selenium.webdriver.edge.service import Service as EdgeService
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.support.ui import Select, WebDriverWait


BASE_DIR = Path(__file__).resolve().parent
DEFAULT_DB_PATH = BASE_DIR / "canteen.db"
MENU_URL = "https://nhss.onlinevidyalaya.net/Pages/StudentManagement/CanteenMenu.aspx"
LOGIN_URL_PART = "/Pages/BaseFramework/Security/Login.aspx"
DATE_RE = re.compile(r"\b(\d{1,2})[/-](\d{1,2})[/-](\d{2,4})\b")


def load_dotenv(path: Path) -> None:
    if not path.exists():
        return
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        os.environ.setdefault(key.strip(), value.strip().strip('"').strip("'"))


load_dotenv(BASE_DIR / ".env")


@dataclass(frozen=True)
class Settings:
    username: str
    password: str
    canteen_name: str
    break_name: str
    database_path: Path
    whatsapp_api_url: str
    whatsapp_phone: str
    whatsapp_group_names: list[str]  # list of group names to send to
    whatsapp_device_id: str
    whatsapp_basic_auth: tuple[str, str] | None
    headless: bool
    edge_binary: str | None
    edge_driver_path: str | None

    @classmethod
    def from_env(cls) -> "Settings":
        auth_value = os.getenv("WHATSAPP_BASIC_AUTH", "").strip()
        auth = None
        if auth_value:
            if ":" not in auth_value:
                raise ValueError("WHATSAPP_BASIC_AUTH must be in username:password format.")
            user, password = auth_value.split(":", 1)
            auth = (user, password)

        edge_binary = os.getenv("EDGE_BINARY", "").strip() or None
        if edge_binary is None:
            default_edge = Path("C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe")
            if default_edge.exists():
                edge_binary = str(default_edge)
        edge_driver_path = os.getenv("EDGE_DRIVER_PATH", "").strip() or None
        if edge_driver_path is None:
            bundled_driver = BASE_DIR.parent / "drivers" / "edgedriver_win64" / "msedgedriver.exe"
            if bundled_driver.exists():
                edge_driver_path = str(bundled_driver)

        # Support comma-separated list in WHATSAPP_GROUP_NAMES, or fall back to
        # the legacy single-group WHATSAPP_GROUP_NAME variable.
        multi_raw = os.getenv("WHATSAPP_GROUP_NAMES", "").strip()
        if multi_raw:
            group_names = [g.strip() for g in multi_raw.split(",") if g.strip()]
        else:
            single = os.getenv("WHATSAPP_GROUP_NAME", "Navrachana Sama Canteen").strip()
            group_names = [single] if single else []

        return cls(
            username=os.getenv("VIDYALAYA_USERNAME", "20024B"),
            password=os.getenv("VIDYALAYA_PASSWORD", "784781"),
            canteen_name=os.getenv("VIDYALAYA_CANTEEN", "NHSS-CANTEEN"),
            break_name=os.getenv("VIDYALAYA_BREAK", "Lunch Break"),
            database_path=Path(os.getenv("CANTEEN_DB_PATH", str(DEFAULT_DB_PATH))),
            whatsapp_api_url=os.getenv("WHATSAPP_API_URL", "http://localhost:3000/send/message"),
            whatsapp_phone=os.getenv("WHATSAPP_PHONE", "120363295006728236"),
            whatsapp_group_names=group_names,
            whatsapp_device_id=os.getenv("WHATSAPP_DEVICE_ID", "").strip(),
            whatsapp_basic_auth=auth,
            headless=os.getenv("EDGE_HEADLESS", "1").lower() not in {"0", "false", "no"},
            edge_binary=edge_binary,
            edge_driver_path=edge_driver_path,
        )


def log(message: str) -> None:
    print(f"[{datetime.now().strftime('%H:%M:%S')}] {message}", flush=True)


def normalize_date(date_text: str) -> str:
    match = DATE_RE.search(date_text)
    if not match:
        raise ValueError(f"Could not find a date in: {date_text!r}")
    day, month, year = match.groups()
    if len(year) == 2:
        year = "20" + year
    return datetime(int(year), int(month), int(day)).strftime("%Y-%m-%d")


def format_food(food_text: str) -> str:
    cleaned = food_text.replace("\r", "\n")
    items = []
    for raw_line in cleaned.split("\n"):
        item = re.sub(r"\s+", " ", raw_line).strip(" -\t")
        if item:
            items.append(item.title())
    return "\n".join(f"{index}) {item}" for index, item in enumerate(items, start=1))


def init_database(path: Path) -> sqlite3.Connection:
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(path)
    conn.execute(
        """
        CREATE TABLE IF NOT EXISTS canteen (
            date_field DATE PRIMARY KEY,
            text_value TEXT NOT NULL
        );
        """
    )
    conn.commit()
    return conn


def build_edge_driver(settings: Settings) -> webdriver.Edge:
    options = EdgeOptions()
    if settings.headless:
        options.add_argument("--headless=new")
    options.add_argument("--disable-gpu")
    options.add_argument("--disable-dev-shm-usage")
    options.add_argument("--no-sandbox")
    options.add_argument("--window-size=1366,900")
    if settings.edge_binary:
        options.binary_location = settings.edge_binary
    service = EdgeService(executable_path=settings.edge_driver_path) if settings.edge_driver_path else None
    return webdriver.Edge(service=service, options=options)


def wait_for_page(driver: webdriver.Edge, timeout: int = 30) -> WebDriverWait:
    return WebDriverWait(driver, timeout)


def first_present(driver: webdriver.Edge, selectors: Iterable[tuple[str, str]], timeout: int = 20):
    wait = wait_for_page(driver, timeout)
    last_error: Exception | None = None
    for by, selector in selectors:
        try:
            return wait.until(EC.presence_of_element_located((by, selector)))
        except TimeoutException as exc:
            last_error = exc
    if last_error:
        raise last_error
    raise TimeoutException("No selectors were provided.")


def select_dropdown(driver: webdriver.Edge, selectors: Iterable[tuple[str, str]], visible_text: str) -> None:
    element = first_present(driver, selectors)
    dropdown = Select(element)
    try:
        dropdown.select_by_visible_text(visible_text)
        return
    except Exception:
        wanted = visible_text.casefold()
        for option in dropdown.options:
            if wanted in option.text.casefold():
                dropdown.select_by_visible_text(option.text)
                return
        real_options = [option.text.strip() for option in dropdown.options if option.text.strip() and "select" not in option.text.casefold()]
        if len(real_options) == 1:
            log(f"Using the only available dropdown option: {real_options[0]}")
            dropdown.select_by_visible_text(real_options[0])
            return
        options = ", ".join(option.text.strip() for option in dropdown.options if option.text.strip())
        raise RuntimeError(f"Could not select {visible_text!r}. Available options: {options}")


def login_if_needed(driver: webdriver.Edge, settings: Settings) -> None:
    if LOGIN_URL_PART not in driver.current_url and driver.title.casefold() != "login":
        return

    log("Login page detected.")
    wait = wait_for_page(driver, 30)
    wait.until(EC.presence_of_element_located((By.ID, "userNameTextBox"))).send_keys(settings.username)
    driver.find_element(By.ID, "passwordTextBox").send_keys(settings.password)

    remember_boxes = driver.find_elements(By.ID, "chkRememberMe")
    if remember_boxes and not remember_boxes[0].is_selected():
        remember_boxes[0].click()

    driver.find_element(By.ID, "loginButton").click()
    wait.until(lambda d: LOGIN_URL_PART not in d.current_url)


def wait_for_document_ready(driver: webdriver.Edge, timeout: int = 15) -> None:
    wait_for_page(driver, timeout).until(lambda d: d.execute_script("return document.readyState") == "complete")


def click_search(driver: webdriver.Edge) -> None:
    locators = [
        (By.ID, "ctl00_CP_SearchButton"),
        (By.CSS_SELECTOR, "input[type='submit'][value*='Search'], button[id*='Search']"),
    ]
    for _ in range(3):
        wait_for_document_ready(driver)
        for by, selector in locators:
            buttons = driver.find_elements(by, selector)
            if not buttons:
                continue
            try:
                wait_for_page(driver, 10).until(EC.element_to_be_clickable((by, selector))).click()
                return
            except StaleElementReferenceException:
                time.sleep(1)
                break
    log("Search button was not found or could not be clicked; trying to parse the currently loaded menu.")


def extract_from_grid(driver: webdriver.Edge) -> list[dict[str, str]]:
    tables = driver.find_elements(By.CSS_SELECTOR, "#ctl00_CP_FoodItemMasterGridView, table[id*='FoodItem'], table[id*='Menu'], table")
    records: list[dict[str, str]] = []

    for table in tables:
        rows = table.find_elements(By.CSS_SELECTOR, "tbody tr, tr")
        for row in rows:
            cells = row.find_elements(By.CSS_SELECTOR, "td, th")
            if len(cells) < 2:
                continue

            row_text = row.text.strip()
            date_match = DATE_RE.search(row_text)
            if not date_match:
                continue

            food_text = ""
            textareas = row.find_elements(By.TAG_NAME, "textarea")
            if textareas:
                food_text = textareas[0].get_attribute("value") or textareas[0].get_attribute("innerText") or ""

            if not food_text:
                candidates = []
                for cell in cells:
                    text = cell.text.strip()
                    if not text or DATE_RE.search(text):
                        continue
                    if text.casefold() in {"date", "day", "canteen", "break", "food item", "food items"}:
                        continue
                    candidates.append(text)
                food_text = max(candidates, key=len, default="")

            if food_text.strip():
                records.append({"date": normalize_date(row_text), "food": format_food(food_text)})

    return unique_records(records)


def extract_from_cards(driver: webdriver.Edge) -> list[dict[str, str]]:
    records: list[dict[str, str]] = []
    candidates = driver.find_elements(By.CSS_SELECTOR, "[id*='Menu'], [id*='Food'], .card, .row, li, div")
    for element in candidates:
        text = element.text.strip()
        if len(text) < 8 or len(text) > 600 or not DATE_RE.search(text):
            continue

        lines = [line.strip() for line in text.splitlines() if line.strip()]
        food_lines = [line for line in lines if not DATE_RE.search(line) and line.casefold() not in {"date", "food", "menu"}]
        food_text = "\n".join(food_lines)
        if food_text:
            records.append({"date": normalize_date(text), "food": format_food(food_text)})
    return unique_records(records)


def unique_records(records: list[dict[str, str]]) -> list[dict[str, str]]:
    by_date: dict[str, str] = {}
    for record in records:
        if record["date"] not in by_date or len(record["food"]) > len(by_date[record["date"]]):
            by_date[record["date"]] = record["food"]
    return [{"date": date_value, "food": food} for date_value, food in sorted(by_date.items())]


def scrape_menu(settings: Settings) -> list[dict[str, str]]:
    driver = build_edge_driver(settings)
    try:
        log("Opening Vidyalaya canteen menu in Microsoft Edge.")
        driver.get(MENU_URL)
        login_if_needed(driver, settings)
        driver.get(MENU_URL)

        select_dropdown(
            driver,
            [
                (By.ID, "ctl00_CP_SelectCanteenDropDownList"),
                (By.CSS_SELECTOR, "select[id*='Canteen']"),
            ],
            settings.canteen_name,
        )
        time.sleep(1)
        select_dropdown(
            driver,
            [
                (By.ID, "ctl00_CP_BreakTypeDropDownList"),
                (By.CSS_SELECTOR, "select[id*='Break']"),
            ],
            settings.break_name,
        )

        click_search(driver)

        time.sleep(2)
        records = extract_from_grid(driver)
        if not records:
            records = extract_from_cards(driver)
        if not records:
            raise RuntimeError("No dated canteen menu rows were found after search.")
        return records
    finally:
        driver.quit()


def save_records(conn: sqlite3.Connection, records: list[dict[str, str]]) -> None:
    conn.executemany(
        """
        INSERT INTO canteen (date_field, text_value)
        VALUES (?, ?)
        ON CONFLICT(date_field) DO UPDATE SET text_value = excluded.text_value;
        """,
        [(record["date"], record["food"]) for record in records],
    )
    conn.commit()


def get_record(conn: sqlite3.Connection, target_date: str) -> tuple[str, str] | None:
    cursor = conn.execute("SELECT date_field, text_value FROM canteen WHERE date_field = ?", (target_date,))
    return cursor.fetchone()


def build_message(record: tuple[str, str]) -> str:
    date_obj = datetime.strptime(record[0], "%Y-%m-%d")
    day_name = date_obj.strftime("%A")
    return f"""*Navrachana Sama Canteen Bot*

*Date:* {record[0]}
*Day:* {day_name}
*Food:*
{record[1]}"""


def whatsapp_base_url(api_url: str) -> str:
    if "/send/message" in api_url:
        return api_url.split("/send/message", 1)[0].rstrip("/") + "/"
    return api_url.rstrip("/") + "/"


def request_headers(settings: Settings) -> dict[str, str]:
    headers = {"Accept": "application/json"}
    if settings.whatsapp_device_id:
        headers["X-Device-Id"] = settings.whatsapp_device_id
    return headers


def walk_json(value: Any) -> Iterable[dict[str, Any]]:
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from walk_json(child)
    elif isinstance(value, list):
        for item in value:
            yield from walk_json(item)


def value_for_key(data: dict[str, Any], wanted: str) -> str:
    for key, value in data.items():
        if key.casefold() == wanted.casefold() and value is not None:
            return str(value)
    return ""


def _fetch_all_groups(settings: Settings) -> list[dict]:
    """Fetch all WhatsApp groups the account belongs to (cached per call)."""
    groups_url = urljoin(whatsapp_base_url(settings.whatsapp_api_url), "user/my/groups")
    response = requests.get(
        groups_url,
        headers=request_headers(settings),
        auth=settings.whatsapp_basic_auth,
        timeout=30,
    )
    response.raise_for_status()
    return list(walk_json(response.json()))


def resolve_group_jid(settings: Settings, group_name: str, cached_groups: list[dict] | None = None) -> str:
    """Resolve a WhatsApp group name to its JID."""
    group_name = group_name.strip()
    if not group_name:
        return settings.whatsapp_phone
    # If the entry is already a JID, use it directly — no lookup needed.
    if group_name.endswith("@g.us"):
        log(f"Using JID directly: {group_name}.")
        return group_name
    if settings.whatsapp_phone.endswith("@g.us"):
        # Legacy: if the phone field already holds a JID, use it for the first group only.
        return settings.whatsapp_phone

    items = cached_groups if cached_groups is not None else _fetch_all_groups(settings)

    matches: list[tuple[str, str]] = []
    for item in items:
        name = value_for_key(item, "Name") or value_for_key(item, "name")
        jid = value_for_key(item, "JID") or value_for_key(item, "jid")
        if name.casefold() == group_name.casefold() and jid.endswith("@g.us"):
            matches.append((name, jid))

    if len(matches) == 1:
        log(f"Resolved WhatsApp group '{matches[0][0]}' to {matches[0][1]}.")
        return matches[0][1]
    if len(matches) > 1:
        choices = ", ".join(jid for _, jid in matches)
        raise RuntimeError(f"Multiple WhatsApp groups named {group_name!r} were found: {choices}. Set WHATSAPP_PHONE to the exact group JID.")
    raise RuntimeError(f"WhatsApp group {group_name!r} was not found. Pair GoWA, ensure the account is in the group, or set WHATSAPP_PHONE to the group JID.")


def send_whatsapp(settings: Settings, message: str, dry_run: bool) -> None:
    if dry_run:
        log("Dry run enabled; WhatsApp message was not sent.")
        groups_display = ", ".join(settings.whatsapp_group_names) if settings.whatsapp_group_names else "(none)"
        log(f"Configured WhatsApp groups: {groups_display}")
        print(message)
        return

    headers = request_headers(settings)

    # Only fetch the group list if at least one entry is a human-readable name.
    cached_groups: list[dict] | None = None
    if any(not n.strip().endswith("@g.us") for n in settings.whatsapp_group_names):
        cached_groups = _fetch_all_groups(settings)

    for group_name in settings.whatsapp_group_names:
        recipient = resolve_group_jid(settings, group_name, cached_groups)
        payload = {"phone": recipient, "message": message}
        response = requests.post(
            settings.whatsapp_api_url,
            json=payload,
            headers=headers,
            auth=settings.whatsapp_basic_auth,
            timeout=30,
        )
        response.raise_for_status()
        log(f"WhatsApp notification sent to '{group_name}' ({recipient}).")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Scrape Vidyalaya canteen menu and send it through WhatsApp.")
    parser.add_argument("--dry-run", action="store_true", help="Scrape/cache data and print the message without sending WhatsApp.")
    parser.add_argument("--refresh", action="store_true", help="Scrape the website even if the target date already exists in SQLite.")
    parser.add_argument("--target-date", help="Target date in YYYY-MM-DD format. Defaults to tomorrow.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    settings = Settings.from_env()
    target_date = args.target_date or (datetime.now() + timedelta(days=1)).strftime("%Y-%m-%d")

    conn = init_database(settings.database_path)
    try:
        record = None if args.refresh else get_record(conn, target_date)
        if record:
            log(f"Found cached menu for {target_date}.")
        else:
            log(f"No cached menu for {target_date}; scraping Vidyalaya.")
            records = scrape_menu(settings)
            save_records(conn, records)
            log(f"Saved {len(records)} menu records.")
            record = get_record(conn, target_date)

        if not record:
            available = ", ".join(record["date"] for record in scrape_menu(settings))
            raise RuntimeError(f"No menu was found for {target_date}. Available scraped dates: {available}")

        if record[1].strip().casefold() == "holiday":
            log(f"{target_date} is marked as HOLIDAY; no WhatsApp message sent.")
            return 0

        send_whatsapp(settings, build_message(record), args.dry_run)
        return 0
    finally:
        conn.close()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        log(f"ERROR: {exc}")
        raise
