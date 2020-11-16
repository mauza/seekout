from seekout.objects.product import Product
from seekout.parsers.generic import ProductSearchPage
from seekout.parsers.utils import parse_price


def parse_newegg_product(soup):
    title = soup.find("a", {"class": "item-title"}).text
    url = soup.find("a", {"class": "item-title"})["href"]
    price = parse_price(soup.find("li", {"class": "price-current"}).text)
    if price:
        in_stock = True
    else:
        in_stock = False
    manufacturer_url = soup.find("a", {"class": "item-brand"})
    if manufacturer_url:
        manufacturer = (
            manufacturer_url.get("href").split("newegg.com/")[1].split("/")[0]
        )
    else:
        manufacturer = None
    rating_link = soup.find("a", {"class": "item-rating"})
    if rating_link:
        rating = rating_link.get("title").split(" + ")[1]
    else:
        rating = None
    product = Product(
        name=title,
        price=price,
        rating=rating,
        manufacturer=manufacturer,
        url=url,
        in_stock=in_stock,
    )
    return product


class NeweggSearch(ProductSearchPage):
    base_url = "https://www.newegg.com"
    categories = {"gpu": "100007709"}

    @staticmethod
    def search_url(self, text, category):
        parsed_text = text.replace(" ", "+").lower()
        category_id = self.categories.get(category)
        return f"{self.base_url}/p/pl?d={parsed_text}&N={category_id}"

    def _parse_page(self):
        raw_products = self._get_products()
        products = list(map(parse_newegg_product, raw_products))
        self.products += products

    def _get_products(self):
        return self.soup.find_all("div", {"class": "item-container"})
