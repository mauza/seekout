import os

from seekout.parsers.newegg import NeweggSearch

CUR_DIR = os.path.dirname(os.path.abspath(__file__))


def test_newegg_search():
    html_path = os.path.join(CUR_DIR, "fixtures/newegg_search_page.html")
    with open(html_path, "r") as f:
        html = f.read()
    newegg_search = NeweggSearch(html)
    assert len(newegg_search.products) == 36
    assert newegg_search.products[0].manufacturer == "EVGA"
