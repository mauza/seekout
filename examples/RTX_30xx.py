import os
from time import sleep
from dotenv import load_dotenv

from seekout.drivers.selenium import SeleniumDriver, DriverType
from seekout.parsers.newegg import NeweggSearch
from seekout.parsers.bestbuy import BestBuySearch
from seekout.alerts import sms

load_dotenv()
SMTP_HOST = os.getenv("SMTP_HOST")
SMTP_PORT = int(os.getenv("SMTP_PORT"))
SMTP_USERNAME = os.getenv("SMTP_USERNAME")
SMTP_PASSWORD = os.getenv("SMTP_PASSWORD")
RECIPIENTS_LIST = [os.getenv("SMS_RECIPIENT")]

WEBDRIVER_PATH = "geckodriver"
BINARY = "/usr/bin/firefox"
DRIVER = SeleniumDriver(BINARY, WEBDRIVER_PATH, DriverType.FIREFOX)


def main():
    search_terms = [
        ("RTX 3070", "gpu"),
        ("RTX 3080", "gpu"),
        ("Ryzen 9 5950x", "cpu"),
        ("Ryzen 9 5900x", "cpu"),
    ]
    print(f"Starting search for: {search_terms}")

    while True:
        for term in search_terms:
            newegg_url = NeweggSearch.search_url(term[0], term[1])
            bestbuy_url = BestBuySearch.search_url(term[0], term[1])

            newegg_products = NeweggSearch(DRIVER.get_html(newegg_url)).products
            bestbuy_products = BestBuySearch(DRIVER.get_html(bestbuy_url)).products

            in_stock_products = [
                product
                for product in newegg_products + bestbuy_products
                if product.in_stock
            ]
            if in_stock_products:
                print(in_stock_products)
                message_text = sms.create_msg_text(in_stock_products)
                sms.sms_notification(
                    message_text,
                    RECIPIENTS_LIST,
                    SMTP_USERNAME,
                    SMTP_PASSWORD,
                    SMTP_HOST,
                    SMTP_PORT,
                )
        sleep(1)


if __name__ == "__main__":
    main()
