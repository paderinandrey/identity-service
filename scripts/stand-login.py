#!/usr/bin/env python3
"""Проходит SAML-вход на стенде так, как это делает браузер, и печатает
значение сессионной cookie.

Зачем отдельный инструмент вместо curl: на хостах `*.localhost` Keycloak
считает контекст безопасным и ставит свои куки с флагом `Secure`. Браузеры
их принимают (loopback — secure context по стандарту), а curl по http
отбрасывает, и вход падает на «Restart login cookie not found». Здесь это
поведение браузера воспроизведено политикой cookiejar.
"""

import http.cookiejar
import html
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


class LoopbackIsSecure(http.cookiejar.DefaultCookiePolicy):
    """Браузеры считают http://*.localhost безопасным контекстом."""

    def return_ok_secure(self, cookie, request):
        return True


def main() -> int:
    host = sys.argv[1] if len(sys.argv) > 1 else "identity.localhost"
    email = sys.argv[2] if len(sys.argv) > 2 else "qa@example.com"
    password = sys.argv[3] if len(sys.argv) > 3 else "password"
    next_path = sys.argv[4] if len(sys.argv) > 4 else "/debug/echo"

    jar = http.cookiejar.CookieJar(policy=LoopbackIsSecure())
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            raise urllib.error.HTTPError(req.full_url, code, msg, headers, fp)

    no_redirect = urllib.request.build_opener(
        NoRedirect(), urllib.request.HTTPCookieProcessor(jar)
    )

    init = f"http://{host}/auth/saml/init?next={urllib.parse.quote(next_path)}"
    try:
        no_redirect.open(init)
        print("FAIL: /auth/saml/init не отдал редирект на IdP", file=sys.stderr)
        return 1
    except urllib.error.HTTPError as err:
        sso_url = err.headers["Location"]

    page = opener.open(sso_url).read().decode()
    match = re.search(r'id="kc-form-login"[^>]*action="([^"]+)"', page)
    if not match:
        print("FAIL: форма входа Keycloak не найдена", file=sys.stderr)
        return 1
    action = html.unescape(match.group(1))

    data = urllib.parse.urlencode({"username": email, "password": password}).encode()
    page = opener.open(action, data).read().decode()
    saml = re.search(r'name="SAMLResponse" value="([^"]+)"', page)
    if not saml:
        hint = re.search(r'<p class="instruction">([^<]{0,160})', page)
        print(
            f"FAIL: Keycloak не вернул SAMLResponse ({hint.group(1).strip() if hint else 'без пояснения'})",
            file=sys.stderr,
        )
        return 1
    relay = re.search(r'name="RelayState" value="([^"]*)"', page)

    acs_data = urllib.parse.urlencode(
        {
            "SAMLResponse": html.unescape(saml.group(1)),
            "RelayState": html.unescape(relay.group(1)) if relay else "",
        }
    ).encode()
    try:
        no_redirect.open(f"http://{host}/auth/saml/acs", acs_data)
        print("FAIL: ACS не отдал редирект", file=sys.stderr)
        return 1
    except urllib.error.HTTPError as err:
        if err.code != 302:
            print(f"FAIL: ACS ответил {err.code}", file=sys.stderr)
            return 1
        landed = err.headers["Location"]

    session = next(
        (c.value for c in jar if c.name.startswith("__identity_session")), None
    )
    if not session:
        print("FAIL: сессионная cookie не выдана", file=sys.stderr)
        return 1

    # stdout читает вызывающий скрипт, диагностика идёт в stderr.
    print(f"вход как {email}, ACS увёл на {landed}", file=sys.stderr)
    print(session)
    return 0


if __name__ == "__main__":
    sys.exit(main())
