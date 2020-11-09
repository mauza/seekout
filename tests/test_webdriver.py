from seekout.drivers.selenium import DriverType, SeleniumDriver


def test_selenium_driver():
    # These will be environment specific
    webdriver_path = "geckodriver"
    binary = "/usr/bin/firefox"

    driver = SeleniumDriver(binary, webdriver_path, DriverType.FIREFOX)
    url = "https://google.com"
    html = driver.get_html(url)
    assert "google" in html.lower()
