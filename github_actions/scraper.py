import os
import re
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path

from selenium import webdriver
from selenium.common.exceptions import StaleElementReferenceException, TimeoutException
from selenium.webdriver.common.by import By
from selenium.webdriver.chrome.options import Options as ChromeOptions
from selenium.webdriver.chrome.service import Service as ChromeService
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.support.ui import Select, WebDriverWait

BASE_DIR = Path(__file__).resolve().parent
MENU_URL = "https://nhss.onlinevidyalaya.net/Pages/StudentManagement/CanteenMenu.aspx"
LOGIN_URL_PART = "/Pages/BaseFramework/Security/Login.aspx"
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

def login_if_needed(driver: webdriver.Chrome) -> None:
    if LOGIN_URL_PART not in driver.current_url and driver.title.casefold() != "login":
        return

    log("Login page detected.")
    wait = wait_for_page(driver, 30)
    wait.until(EC.presence_of_element_located((By.ID, "userNameTextBox"))).send_keys(os.environ["VIDYALAYA_USERNAME"])
    driver.find_element(By.ID, "passwordTextBox").send_keys(os.environ["VIDYALAYA_PASSWORD"])

    remember_boxes = driver.find_elements(By.ID, "chkRememberMe")
    if remember_boxes and not remember_boxes[0].is_selected():
        remember_boxes[0].click()

    driver.find_element(By.ID, "loginButton").click()
    wait.until(lambda d: LOGIN_URL_PART not in d.current_url)

def wait_for_document_ready(driver: webdriver.Chrome, timeout: int = 15) -> None:
    wait_for_page(driver, timeout).until(lambda d: d.execute_script("return document.readyState") == "complete")

def click_search(driver: webdriver.Chrome) -> None:
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
    log("Search button was not found.")

def extract_from_grid(driver: webdriver.Chrome) -> list[dict[str, str]]:
    tables = driver.find_elements(By.CSS_SELECTOR, "#ctl00_CP_FoodItemMasterGridView, table[id*='FoodItem'], table[id*='Menu'], table")
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
        driver.get(MENU_URL)
        login_if_needed(driver)
        driver.get(MENU_URL)

        select_dropdown(driver, [(By.ID, "ctl00_CP_SelectCanteenDropDownList"), (By.CSS_SELECTOR, "select[id*='Canteen']")], canteen_name)
        time.sleep(1)
        select_dropdown(driver, [(By.ID, "ctl00_CP_BreakTypeDropDownList"), (By.CSS_SELECTOR, "select[id*='Break']")], break_name)

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

def build_message(record: dict) -> str:
    date_obj = datetime.strptime(record["date"], "%Y-%m-%d")
    day_name = date_obj.strftime("%A")
    return f"""*Navrachana Sama Canteen Bot*

*Date:* {record["date"]}
*Day:* {day_name}
*Food:*
{record["food"]}"""

def main():
    target_date = (datetime.now() + timedelta(days=1)).strftime("%Y-%m-%d")
    log(f"Target Date for GitHub Actions: {target_date}")
    
    records = scrape_menu()
    record = next((r for r in records if r["date"] == target_date), None)
    
    if not record:
        available = ", ".join(r["date"] for r in records)
        log(f"No menu was found for {target_date}. Available scraped dates: {available}")
        sys.exit(0) # Do not fail the action if there is no menu for tomorrow
        
    if record["food"].strip().casefold() == "holiday":
        log(f"{target_date} is marked as HOLIDAY; no message created.")
        sys.exit(0)

    msg = build_message(record)
    with open("github_actions/message_to_send.txt", "w", encoding="utf-8") as f:
        f.write(msg)
    log("Message successfully written to message_to_send.txt")

if __name__ == "__main__":
    main()
