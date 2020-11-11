import re


def parse_price(price_text):
    price_list = re.findall(r"\d{1,3}(?:[.,]\d{3})*(?:[.,]\d{2})", price_text)
    if len(price_list) > 0:
        price_str = price_list[0]
    else:
        return None
    return float(price_str.replace(",", ""))
