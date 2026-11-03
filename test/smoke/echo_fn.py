import pandas as pd


def handler(event):
    products = event.get("products") or []
    return {"received": products, "count": int(pd.Series(products).size)}
