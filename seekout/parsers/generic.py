from bs4 import BeautifulSoup


class NotImplementedError(Exception):
    pass


class Page:
    html = ""
    soup = None
    parser = "html.parser"

    def __init__(self, html):
        self.html = html
        self.soup = BeautifulSoup(html, self.parser)
        self._parse_page()

    def _parse_page(self):
        raise NotImplementedError("_parse_page not implemented")


class ProductSearchPage(Page):
    products = []
