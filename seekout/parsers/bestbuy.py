import re
from seekout.objects.product import Product
from seekout.parsers.generic import ProductSearchPage
from seekout.parsers.utils import parse_price

BASE_URL = "bestbuy.com"


def parse_rating(text):
    result = re.findall(r"\d\.\d", text)
    if result:
        return result[0]
    return None


def parse_bestbuy_product(soup):
    title = soup.find("div", {"class": "sku-title"}).text
    url_path = soup.find("h4", {"class": "sku-header"}).find("a")["href"]
    price = parse_price(soup.find("div", {"class": "priceView-customer-price"}).text)
    in_stock = (
        "sold out"
        not in soup.find("button", {"class": "add-to-cart-button"}).text.lower()
    )
    manufacturer = None
    rating = parse_rating(
        soup.find("div", {"class": "ugc-ratings-reviews"})
        .find("p", {"class": "sr-only"})
        .text
    )
    product = Product(
        name=title,
        price=price,
        rating=rating,
        manufacturer=manufacturer,
        url=f"{BASE_URL}{url_path}",
        in_stock=in_stock,
    )
    return product


class BestBuySearch(ProductSearchPage):
    base_url = BASE_URL
    categories = {"cpu": "category_facet%3DCPUs%20%2F%20Processors~abcat0507010"}

    @staticmethod
    def search_url(self, text, category):
        parsed_text = text.replace(" ", "%20").lower()
        category_id = self.categories.get(category)
        return f"{self.base_url}/site/searchpage.jsp?st={parsed_text}&qp={category_id}"

    def _parse_page(self):
        raw_products = self._get_products()
        products = list(map(parse_bestbuy_product, raw_products))
        self.products += products

    def _get_products(self):
        return self.soup.find_all("li", {"class": "sku-item"})
