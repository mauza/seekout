from seekout.objects.product import Product


def test_product():
    product = Product(
        name="Sup",
        price="$3.50",
        rating="5",
        manufacturer="mauza",
        url="mauza.net",
        in_stock=True,
    )
    assert product.name == "Sup"
