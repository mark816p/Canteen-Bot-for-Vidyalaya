import os
import re
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

from selenium import webdriver
from selenium.common.exceptions import StaleElementReferenceException, TimeoutException
from selenium.webdriver.common.by import By
from selenium.webdriver.chrome.options import Options as ChromeOptions
from selenium.webdriver.chrome.service import Service as ChromeService
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.support.ui import Select, WebDriverWait

BASE_DIR = Path(__file__).resolve().parent
MENU_URL = "https://nhss.onlinevidyalaya.net/MyCommunication/CanteenMenu"
LOGIN_URL = "https://nhss.onlinevidyalaya.net/login?orgGroupId=1965"
LOGIN_URL_PART = "/login"
DATE_RE = re.compile(r"\b(\d{1,2})[/-](\d{1,2})[/-](\d{2,4})\b")

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

def build_chrome_driver() -> webdriver.Chrome:
    options = ChromeOptions()
    options.add_argument("--headless=new")
    options.add_argument("--disable-gpu")
    options.add_argument("--disable-dev-shm-usage")
    options.add_argument("--no-sandbox")
    options.add_argument("--window-size=1366,900")
    # In GitHub actions, chromedriver is already in PATH
    return webdriver.Chrome(options=options)

def wait_for_page(driver: webdriver.Chrome, timeout: int = 30) -> WebDriverWait:
    return WebDriverWait(driver, timeout)

def first_present(driver: webdriver.Chrome, selectors: list[tuple[str, str]], timeout: int = 20):
    wait = wait_for_page(driver, timeout)
    last_error = None
    for by, selector in selectors:
        try:
            return wait.until(EC.presence_of_element_located((by, selector)))
        except TimeoutException as exc:
            last_error = exc
    if last_error:
        raise last_error
    raise TimeoutException("No selectors were provided.")

def select_dropdown(driver: webdriver.Chrome, selectors: list[tuple[str, str]], visible_text: str) -> None:
    for attempt in range(5):
        try:
            wait_for_document_ready(driver)
            element = first_present(driver, selectors)
            dropdown = Select(element)
            option_texts = [option.text.strip() for option in dropdown.options]
            log(f"Dropdown options for {visible_text!r}: {option_texts}")

            # The portal lazily populates dependent options when a native
            # select is opened. Selecting the canteen itself uses its real item.
            if len(option_texts) <= 1 or not any(visible_text.casefold() in text.casefold() for text in option_texts):
                try:
                    element.click()
                except StaleElementReferenceException:
                    # Opening a filter can replace its DOM node. The wait
                    # below reads fresh elements on every poll.
                    log(f"Portal refreshed the {visible_text!r} filter while opening it.")

                def options_ready(d):
                    wanted = visible_text.casefold()
                    for by, selector in selectors:
                        for current in d.find_elements(by, selector):
                            try:
                                current_select = Select(current)
                                texts = [option.text.strip() for option in current_select.options]
                                selected = current_select.first_selected_option.text.strip()
                                if wanted in selected.casefold() or any(wanted in text.casefold() for text in texts):
                                    return True
                            except StaleElementReferenceException:
                                continue
                    return False

                wait_for_page(driver, 30).until(options_ready)
                element = first_present(driver, selectors)
                dropdown = Select(element)
            try:
                dropdown.select_by_visible_text(visible_text)
                return
            except (StaleElementReferenceException, TimeoutException):
                raise
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
        except (StaleElementReferenceException, TimeoutException) as exc:
            if attempt == 4:
                select_states = driver.execute_script("""
                    return Array.from(document.querySelectorAll('select')).map((select) => ({
                        id: select.id,
                        value: select.value,
                        options: Array.from(select.options).map((option) => option.text.trim())
                    }));
                """)
                log(f"Dropdown diagnostic at {driver.current_url}: {select_states}")
                raise exc
            log(f"Retrying dropdown selection for {visible_text!r} after {type(exc).__name__} (attempt {attempt + 1}/5)...")
            time.sleep(2)

def login_if_needed(driver: webdriver.Chrome) -> None:
    wait = wait_for_page(driver, 30)
    wait.until(lambda d:
        LOGIN_URL_PART in d.current_url.casefold()
        or d.find_elements(By.CSS_SELECTOR, "input[formcontrolname='username']")
        or d.find_elements(By.ID, "login-org")
    )
    if LOGIN_URL_PART not in driver.current_url.casefold():
        log("Existing Vidyalaya session is authenticated.")
        return

    log("Login page detected.")

    def visible_login_field(selectors):
        for by, selector in selectors:
            for element in driver.find_elements(by, selector):
                if element.is_displayed():
                    return element
        return False

    username = wait.until(lambda _: visible_login_field([
        (By.CSS_SELECTOR, "input[formcontrolname='username']"),
        (By.CSS_SELECTOR, "input[autocomplete='username']"),
        (By.CSS_SELECTOR, "input[name*='user']"),
        (By.CSS_SELECTOR, "input[id*='user']"),
        (By.CSS_SELECTOR, "input[placeholder*='Username']"),
        (By.CSS_SELECTOR, "input:not([type])"),
        (By.CSS_SELECTOR, "input[type='text']"),
    ]))
    password = wait.until(lambda _: visible_login_field([
        (By.CSS_SELECTOR, "input[formcontrolname='password']"),
        (By.CSS_SELECTOR, "input[autocomplete='current-password']"),
        (By.CSS_SELECTOR, "input[type='password']"),
    ]))
    username.send_keys(os.environ["VIDYALAYA_USERNAME"])
    password.send_keys(os.environ["VIDYALAYA_PASSWORD"])
    log("Submitting portal sign-in form.")
    sign_in = driver.find_elements(By.XPATH, "//button[normalize-space()='Sign In']")
    (sign_in[0] if sign_in else driver.find_element(By.CSS_SELECTOR, "button[type='submit']")).click()
    try:
        wait.until(lambda d: LOGIN_URL_PART not in d.current_url.casefold())
    except TimeoutException:
        login_state = driver.execute_script("""
            const describe = (element) => ({
                tag: element.tagName,
                type: element.getAttribute('type'),
                id: element.id,
                name: element.getAttribute('name'),
                formControlName: element.getAttribute('formcontrolname'),
                placeholder: element.getAttribute('placeholder'),
                autocomplete: element.getAttribute('autocomplete'),
                hasValue: Boolean(element.value),
                text: element.innerText.trim(),
                disabled: element.disabled
            });
            return {
                url: location.href,
                inputs: Array.from(document.querySelectorAll('input')).filter((e) => e.offsetParent !== null).map(describe),
                buttons: Array.from(document.querySelectorAll('button')).filter((e) => e.offsetParent !== null).map(describe),
                forms: Array.from(document.forms).map((form) => ({ action: form.action, valid: form.checkValidity() })),
                validation: Array.from(document.querySelectorAll('.alert, .invalid-feedback, [role="alert"]'))
                    .filter((element) => element.innerText.trim())
                    .map((element) => element.innerText.trim())
            };
        """)
        log(f"Portal sign-in did not leave the login route. Control diagnostic: {login_state}")
        raise

def wait_for_document_ready(driver: webdriver.Chrome, timeout: int = 15) -> None:
    wait_for_page(driver, timeout).until(lambda d: d.execute_script("return document.readyState") == "complete")

def click_search(driver: webdriver.Chrome) -> None:
    locators = [
        (By.ID, "Search"),
        (By.CSS_SELECTOR, "button[id*='Search']"),
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
    log("Search button was not found.")

def extract_from_grid(driver: webdriver.Chrome) -> list[dict[str, str]]:
    tables = driver.find_elements(By.CSS_SELECTOR, "#canteenMenu, table[id*='FoodItem'], table[id*='Menu'], table")
    records = []

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

            if table.get_attribute("id") == "canteenMenu":
                item_cell = cells[-1]
                fields = item_cell.find_elements(By.CSS_SELECTOR, "textarea, input")
                food_text = next((field.get_attribute("value") or field.get_attribute("innerText") or "" for field in fields if field.get_attribute("value") or field.get_attribute("innerText")), "")
                if not food_text:
                    food_text = item_cell.text
                if food_text.strip():
                    records.append({"date": normalize_date(cells[0].text), "food": format_food(food_text)})
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

def extract_from_cards(driver: webdriver.Chrome) -> list[dict[str, str]]:
    records = []
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
    by_date = {}
    for record in records:
        if record["date"] not in by_date or len(record["food"]) > len(by_date[record["date"]]):
            by_date[record["date"]] = record["food"]
    return [{"date": date_value, "food": food} for date_value, food in sorted(by_date.items())]

def scrape_menu() -> list[dict[str, str]]:
    driver = build_chrome_driver()
    canteen_name = os.environ.get("VIDYALAYA_CANTEEN", "NHSS-CANTEEN")
    break_name = os.environ.get("VIDYALAYA_BREAK", "Lunch Break")
    try:
        log("Opening Vidyalaya canteen menu in Chrome.")
        # A clean GitHub runner has no portal session. Start at the portal's
        # login route so its menu shell cannot be mistaken for authentication.
        driver.get(LOGIN_URL)
        login_if_needed(driver)
        driver.get(MENU_URL)
        log(f"Canteen page loaded at {driver.current_url}.")

        select_dropdown(driver, [(By.ID, "selectcanteen"), (By.CSS_SELECTOR, "select[id*='canteen']")], canteen_name)
        time.sleep(1)
        select_dropdown(driver, [(By.ID, "breaktype"), (By.CSS_SELECTOR, "select[id*='break']")], break_name)

        click_search(driver)
        wait_for_page(driver).until(lambda d: any(DATE_RE.search(row.text) for row in d.find_elements(By.CSS_SELECTOR, "#canteenMenu tbody tr")))
        
        records = extract_from_grid(driver)
        if not records:
            records = extract_from_cards(driver)
        if not records:
            log("No dated canteen menu rows were found after search. Returning empty list.")
            return []
        return records
    finally:
        driver.quit()

def build_message(record: dict) -> str:
    date_obj = datetime.strptime(record["date"], "%Y-%m-%d")
    day_name = date_obj.strftime("%A")
    return f"""*Navrachana Sama Canteen Bot*

*Date:* {record["date"]}
*Day:* {day_name}
*Food:*
{record["food"]}"""

def main():
    ist = timezone(timedelta(hours=5, minutes=30))
    target_date = (datetime.now(ist) + timedelta(days=1)).strftime("%Y-%m-%d")
    log(f"Target Date for GitHub Actions: {target_date}")
    
    try:
        records = scrape_menu()
    except Exception as exc:
        log(f"Menu scrape failed ({type(exc).__name__}: {exc}). Sending the 'Not Updated Yet' fallback.")
        records = []
    record = next((r for r in records if r["date"] == target_date), None)
    
    if not record:
        available = ", ".join(r["date"] for r in records)
        log(f"No menu was found for {target_date}. Available scraped dates: {available}")
        log("Proceeding with 'Not Updated Yet' message.")
        record = {"date": target_date, "food": "1) Not Updated Yet"}
    else:
        log(f"Scraped target menu for {target_date}:\n{record['food']}")
        
    if record["food"].strip().casefold() == "holiday" or "holiday" in record["food"].casefold():
        log(f"{target_date} is marked as HOLIDAY.")
        # Instead of skipping, we just write the holiday message!

    if os.environ.get("ENABLE_WHATSAPP") == "1":
        msg = build_message(record)
        with open("github_actions/message_to_send.txt", "w", encoding="utf-8") as f:
            f.write(msg)
        log("Message successfully written to message_to_send.txt")
    else:
        log("Scraper-only mode: WhatsApp output is disabled.")

if __name__ == "__main__":
    main()
