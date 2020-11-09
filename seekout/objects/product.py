class Product:
    name = None
    price = None
    rating = None
    manufacturer = None
    type = None
    url = None
    in_stock = False

    def __init__(self, name, price, rating, manufacturer, url, in_stock):
        self.name = name
        self.price = price
        self.rating = rating
        self.manufacturer = manufacturer
        self.url = url
        self.in_stock = in_stock
