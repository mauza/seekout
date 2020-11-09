from seekout.objects.product import Product
from seekout.parsers.generic import ProductSearchPage


class NeweggSearch(ProductSearchPage):
    def _parse_page(self):
        self.items.append(Product())
