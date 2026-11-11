#!/usr/bin/env python3
"""Sign in to an openbkn mini that runs with MINI_SAFE=on and print an access token.

  scripts/login.py --password PASSWORD [--account admin] [--url http://127.0.0.1:8000]
                   [--new-password NEW]

For scripts and machines without a browser: it walks the device sign-in the
openbkn CLI and SDK use, answering the sign-in pages itself. A user an
administrator created must change the assigned password at the first sign-in;
pass --new-password to do that here (afterwards NEW is the password).

Use the token as BKN_TOKEN, or `openbkn auth login URL --token TOKEN`.
"""
import argparse
import http.cookiejar
import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

CLIENT_ID = "openbkn-sdk"
DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code"
# Where the browser ends up once the device is approved: bkn-safe's page, or
# Hydra's own when no success page is configured.
DONE_PATHS = ("/device/success", "/oauth2/fallbacks/device/done")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Browser:
    """Requests with cookies, one hop at a time."""

    def __init__(self, base):
        self.base = base.rstrip("/")
        # mini is on this machine: a proxy from the environment would not reach it.
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect,
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, url, form=None):
        """Return (status, location, body) for one request to url."""
        url = urllib.parse.urljoin(self.base + "/", url)
        data = urllib.parse.urlencode(form).encode() if form is not None else None
        try:
            with self.opener.open(urllib.request.Request(url, data=data), timeout=30) as resp:
                return resp.status, resp.headers.get("Location"), resp.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as err:
            return err.code, err.headers.get("Location"), err.read().decode("utf-8", "replace")


def query(url, name):
    values = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query).get(name)
    return values[0] if values else ""


def fail(message):
    sys.exit("login: " + message)


def sign_in(browser, account, password, new_password):
    status, _, body = browser.request(
        "/oauth2/device/auth", {"client_id": CLIENT_ID, "scope": "openid offline", "audience": "bkn-safe"})
    if status != 200:
        fail("device sign-in is not available (%d): %s" % (status, body[:300]))
    device = json.loads(body)
    user_code = device["user_code"]

    # Follow the browser's path: each page that needs an answer gets one.
    url, form = device.get("verification_uri_complete") or device["verification_uri"], None
    challenge = ""
    for _ in range(30):
        status, location, body = browser.request(url, form)
        form = None
        if status in (301, 302, 303, 307):
            if not location:
                fail("redirect without a target from " + url)
            path = urllib.parse.urlsplit(location).path
            if path == "/device" and query(location, "device_challenge"):
                browser.request(location)  # the page sets its cookie
                url = "/device"
                form = {"device_challenge": query(location, "device_challenge"), "user_code": user_code}
            elif path == "/login" and query(location, "login_challenge"):
                challenge = query(location, "login_challenge")
                url, form = "/login", {"login_challenge": challenge, "account": account, "password": password}
            elif path == "/consent" and query(location, "consent_challenge"):
                browser.request(location)
                url = "/consent"
                form = {"consent_challenge": query(location, "consent_challenge"), "decision": "allow"}
            elif path in DONE_PATHS:
                return device
            else:
                url = location
            continue
        if status == 200 and urllib.parse.urlsplit(url).path in DONE_PATHS:
            return device
        if status == 200 and 'name="old_password"' in body:
            # Valid credentials, but the password must be changed first.
            if not new_password:
                fail("the password of %s must be changed at the first sign-in: pass --new-password" % account)
            url, form = "/change-password", {
                "login_challenge": challenge, "account": account, "old_password": password,
                "new_password": new_password, "confirm_password": new_password}
            password, new_password = new_password, ""
            continue
        if status in (200, 401, 403) and 'name="login_challenge"' in body:
            fail("wrong account or password, or the account is disabled")
        fail("unexpected answer %d from %s: %s" % (status, url, " ".join(body.split())[:300]))
    fail("sign-in did not finish")


def token(browser, device):
    interval = max(1, int(device.get("interval", 5)))
    deadline = time.time() + 120
    while time.time() < deadline:
        status, _, body = browser.request("/oauth2/token", {
            "grant_type": DEVICE_GRANT, "device_code": device["device_code"], "client_id": CLIENT_ID})
        answer = json.loads(body) if body.strip().startswith("{") else {}
        if status == 200 and answer.get("access_token"):
            return answer["access_token"]
        if answer.get("error") not in ("authorization_pending", "slow_down"):
            fail("token refused (%d): %s" % (status, body[:300]))
        time.sleep(interval)
    fail("no token within two minutes")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default="http://127.0.0.1:8000", help="mini's address")
    parser.add_argument("--account", default="admin")
    parser.add_argument("--password", required=True)
    parser.add_argument("--new-password", default="", help="set this password if a change is required")
    args = parser.parse_args()
    browser = Browser(args.url)
    device = sign_in(browser, args.account, args.password, args.new_password)
    print(token(browser, device))


if __name__ == "__main__":
    main()
