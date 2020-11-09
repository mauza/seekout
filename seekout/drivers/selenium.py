from enum import Enum
from selenium import webdriver
from selenium.webdriver.firefox.options import Options
from selenium.webdriver.common.desired_capabilities import DesiredCapabilities


class DriverType(str, Enum):
    FIREFOX = "firefox"
    CHROMIUM = "chromium"
    CHROME = "chrome"


class DriverNotImplemented(Exception):
    pass


def firefox_driver(binary: str, webdriver_path: str) -> webdriver:
    cap = DesiredCapabilities().FIREFOX
    cap["marionette"] = True
    options = Options()
    options.headless = True
    options.binary = binary
    return webdriver.Firefox(
        capabilities=cap, options=options, executable_path=webdriver_path
    )


class SeleniumDriver:
    def __init__(self, binary_path: str, webdriver_path: str, driver_type: DriverType):
        self.reload_count = 0
        self.binary = binary_path
        self.webdriver_path = webdriver_path
        self.driver_type = driver_type
        self.driver = self.get_driver()

    def __del__(self):
        self.driver.close()
        self.driver.quit()

    def get_driver(self):
        if self.driver_type == DriverType.FIREFOX:
            return firefox_driver(self.binary, self.webdriver_path)
        else:
            raise DriverNotImplemented(
                f"Driver Type {self.driver_type} not implemented"
            )

    def ensure_fresh_driver(self):
        if self.reload_count > 10:
            self.driver.close()
            self.driver.quit()
            self.driver = self.get_driver()
        else:
            self.reload_count += 1

    def get_html(self, url: str) -> str:
        self.ensure_fresh_driver()
        self.driver.get(url)
        return self.driver.page_source
