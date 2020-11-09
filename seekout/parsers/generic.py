from bs4 import BeautifulSoup


PARSER = "html.parser"


class NotImplementedError(Exception):
    pass


class Page:
    html = ""
    soup = None

    def __init__(self, html):
        self.html = html
        self.soup = BeautifulSoup(html, PARSER)
        self._parse_page()

    def _parse_page(self):
        raise NotImplementedError("_parse_page not implemented")


class ProductSearchPage(Page):
    products = []
